package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func (s *Store) NextInterruptedRecovery(ctx context.Context, generation, owner, reason string) (execution.InterruptedRecovery, error) {
	var recovery execution.InterruptedRecovery
	if generation == "" || owner == "" || strings.TrimSpace(reason) == "" {
		return recovery, fmt.Errorf("interrupted recovery identity, owner and reason required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return recovery, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return recovery, err
	}
	binding, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return recovery, err
	}
	recovery.Binding = binding
	environment, err := environmentWith(ctx, tx, binding.EnvironmentID)
	if err != nil {
		return recovery, err
	}
	var recordedReason, operationID string
	err = tx.QueryRowContext(ctx, `SELECT stage,reason,operation_id FROM execution_recoveries WHERE generation_id=?`, generation).Scan(&recovery.Stage, &recordedReason, &operationID)
	now := time.Now().UTC()
	if errors.Is(err, sql.ErrNoRows) {
		if binding.State != "accepted" && binding.State != "retired" {
			// A failed attachment has already fenced dispatch. Only that
			// durable adoption marker may enter recovery from starting; ordinary
			// process startup cannot use this exception.
			var adopting int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_generations g WHERE g.generation_id=? AND g.lease_owner=? AND ((g.status='starting' AND EXISTS(SELECT 1 FROM idle_execution_adoptions a WHERE a.generation_id=g.generation_id AND a.owner=g.lease_owner) AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.session_id=? AND t.status IN ('leased','running'))) OR (g.status IN ('idle','active') AND EXISTS(SELECT 1 FROM active_execution_adoptions a WHERE a.generation_id=g.generation_id AND a.owner=g.lease_owner AND a.state='adopting')))`, generation, owner, binding.SessionID).Scan(&adopting); err != nil {
				return recovery, err
			}
			var failedStart int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_start_failures f WHERE f.generation_id=? AND f.process_handle=? AND f.epoch=? AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.generation_id=f.generation_id)`, generation, binding.ProcessHandle, binding.Epoch).Scan(&failedStart); err != nil {
				return recovery, err
			}
			var retiring int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_retirements WHERE session_id=? AND state='requested'`, binding.SessionID).Scan(&retiring); err != nil {
				return recovery, err
			}
			if binding.State != "starting" || (retiring != 1 && (binding.Owner != owner || (adopting != 1 && failedStart != 1))) {
				return recovery, execution.ErrBusy
			}
		}
		if binding.StoreID != environment.StoreID || binding.SandboxID != environment.SandboxID || binding.Epoch != environment.Epoch || binding.EnvironmentFence != environment.Fence {
			return recovery, execution.ErrStaleOperation
		}
		var current int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE id=? AND active_generation_id=?`, binding.SessionID, generation).Scan(&current); err != nil {
			return recovery, err
		}
		if current != 1 {
			return recovery, execution.ErrStaleOperation
		}
		var writers int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')`, binding.SessionID).Scan(&writers); err != nil {
			return recovery, err
		}
		if writers != 0 {
			return recovery, execution.ErrBusy
		}
		var stream int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM bridge_streams WHERE backend_id=? AND generation_id=? AND epoch=?`, binding.StoreID, generation, binding.Epoch).Scan(&stream); err != nil {
			return recovery, err
		}
		if stream != 1 {
			return recovery, fmt.Errorf("interrupted execution has no bound retained journal")
		}
		recovery.Stage = "pause"
		operationID = "interrupt-pause:" + generation
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_recoveries(generation_id,environment_id,reason,stage,operation_id,created_at,updated_at) VALUES(?,?,?,'pause',?,?,?)`, generation, environment.ID, reason, operationID, formatTime(now), formatTime(now)); err != nil {
			return recovery, err
		}
		recovery.Operation, err = reserveRecoveryOperationTx(ctx, tx, binding, environment, recovery.Stage, operationID, now)
		if err != nil {
			return recovery, err
		}
	} else if err != nil {
		return recovery, err
	} else {
		if recordedReason != reason {
			return recovery, execution.ErrOperationConflict
		}
		if recovery.Stage == "complete" {
			return recovery, tx.Commit()
		}
		operation, err := operationWith(ctx, tx, operationID)
		if err != nil {
			return recovery, err
		}
		if operation.EnvironmentID != environment.ID || operation.Fence != environment.Fence {
			return recovery, execution.ErrStaleOperation
		}
		recovery.Operation = operation
		if operation.State == execution.OperationFailed {
			return recovery, fmt.Errorf("interrupted recovery step failed: %s", operation.Error)
		}
		if operation.State == execution.OperationSucceeded {
			switch recovery.Stage {
			case "pause":
				if err := verifiedRecoveryLifecycle(operation, environment, "paused", binding.Epoch); err != nil {
					return recovery, err
				}
				// Pause proves writer termination. Retire protocol authority while
				// leaving turn classification pending until journal recovery.
				if _, err := tx.ExecContext(ctx, `UPDATE runtime_generations SET status='failed',error_class='execution_interrupted',failure_reason=?,ended_at=?,lease_owner=NULL,lease_expires_at=NULL WHERE generation_id=?`, reason, formatTime(now), generation); err != nil {
					return recovery, err
				}
				recovery.Stage = "resume"
				operationID = "interrupt-resume:" + generation
			case "resume":
				if err := verifiedRecoveryLifecycle(operation, environment, "running", binding.Epoch+1); err != nil {
					return recovery, err
				}
				recovery.Stage = "replay"
				operationID, _ = execution.JournalReplayRequest(binding, environment)
			case "replay":
				if err := appendInterruptedBoundariesTx(ctx, tx, binding, reason, now); err != nil {
					return recovery, err
				}
				var pending int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM bridge_ingress i LEFT JOIN bridge_projections p ON p.generation_id=i.generation_id AND p.turn_id=i.turn_id AND p.attempt=i.attempt WHERE i.generation_id=? AND i.id>coalesce(p.last_record_id,0)`, generation).Scan(&pending); err != nil {
					return recovery, err
				}
				if pending != 0 {
					break
				} // Return replay action so projection retries.
				if binding.Driver == "pi" && operation.Kind == "recover_journal" {
					operationID = "interrupt-native-restore:" + generation
				} else {
					recovery.Stage = "final_pause"
					operationID = "interrupt-final-pause:" + generation
				}
			case "final_pause":
				if err := verifiedRecoveryLifecycle(operation, environment, "paused", binding.Epoch+1); err != nil {
					return recovery, err
				}
				if err := finishInterruptedRecoveryTx(ctx, tx, binding, reason, now); err != nil {
					return recovery, err
				}
				recovery.Stage = "complete"
				return recovery, tx.Commit()
			default:
				return recovery, execution.ErrOperationConflict
			}
			if operationID != operation.ID {
				if _, err := tx.ExecContext(ctx, `UPDATE execution_recoveries SET stage=?,operation_id=?,updated_at=? WHERE generation_id=?`, recovery.Stage, operationID, formatTime(now), generation); err != nil {
					return recovery, err
				}
				recovery.Operation, err = reserveRecoveryOperationTx(ctx, tx, binding, environment, recovery.Stage, operationID, now)
				if err != nil {
					return recovery, err
				}
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT binding_id FROM egress_route_bindings WHERE generation_id=? ORDER BY binding_id`, generation)
	if err != nil {
		return recovery, err
	}
	for rows.Next() {
		var route string
		if err := rows.Scan(&route); err != nil {
			rows.Close()
			return recovery, err
		}
		recovery.Routes = append(recovery.Routes, route)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return recovery, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_route_bindings SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return recovery, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return recovery, err
	}
	return recovery, tx.Commit()
}

// A crashed producer may never have journaled completion. Once its entire
// retained head is committed, a host failure boundary flushes partial parser
// state without pretending the guest completed successfully.
func appendInterruptedBoundariesTx(ctx context.Context, tx *sql.Tx, binding execution.Binding, reason string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT t.id,coalesce((SELECT max(output_sequence) FROM bridge_ingress i WHERE i.generation_id=t.generation_id AND i.turn_id=t.id AND i.attempt=t.attempt),0) FROM turns t WHERE t.generation_id=? AND t.status IN ('leased','running','failed','canceled') AND NOT EXISTS(SELECT 1 FROM bridge_ingress i WHERE i.generation_id=t.generation_id AND i.turn_id=t.id AND i.attempt=t.attempt AND i.record_key='completion')`, binding.GenerationID)
	if err != nil {
		return err
	}
	type boundary struct{ turn, head int64 }
	var boundaries []boundary
	for rows.Next() {
		var b boundary
		if err := rows.Scan(&b.turn, &b.head); err != nil {
			rows.Close()
			return err
		}
		boundaries = append(boundaries, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range boundaries {
		raw, err := json.Marshal(map[string]any{"status": "failed", "error_class": "execution_interrupted", "error": reason, "final_output_sequence": b.head})
		if err != nil {
			return err
		}
		if _, err := appendBridgeRecordTx(ctx, tx, AppendEventParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, TurnID: &b.turn, Type: "ack_turn_completed", Now: now}, raw); err != nil {
			return err
		}
	}
	return nil
}

func reserveRecoveryOperationTx(ctx context.Context, tx *sql.Tx, binding execution.Binding, environment execution.Environment, stage, id string, now time.Time) (execution.Operation, error) {
	kind := "pause"
	var request any = execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision}}
	if stage == "resume" {
		kind = "resume"
	}
	if stage == "replay" {
		if id == "interrupt-native-restore:"+binding.GenerationID {
			kind = "restore_native"
			var err error
			request, err = nativeRestoreRequestTx(ctx, tx, binding, environment)
			if err != nil {
				return execution.Operation{}, err
			}
		} else {
			kind = "recover_journal"
			_, request = execution.JournalReplayRequest(binding, environment)
		}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return execution.Operation{}, err
	}
	raw, err = normalizedOperationJSON(raw)
	if err != nil {
		return execution.Operation{}, err
	}
	return beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: id, EnvironmentID: environment.ID, Kind: kind, IdempotencyKey: id, ExpectedFence: environment.Fence, Request: raw, Now: now}, raw)
}

func verifiedRecoveryLifecycle(op execution.Operation, environment execution.Environment, state string, epoch uint64) error {
	var observed sandbox.Observation
	if err := json.Unmarshal(op.Result, &observed); err != nil {
		return err
	}
	placement := "active"
	if state == "paused" {
		placement = "lost"
	}
	if observed.State != state || observed.PlacementState != placement || observed.StoreID != environment.StoreID || observed.SandboxID != environment.SandboxID || observed.Epoch != epoch || observed.Epoch != environment.Epoch || observed.MutationRevision != environment.MutationRevision {
		return execution.ErrStaleOperation
	}
	return nil
}

func finishInterruptedRecoveryTx(ctx context.Context, tx *sql.Tx, binding execution.Binding, reason string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT c.command_id,c.turn_id FROM execution_steering q JOIN execution_turn_commands c ON c.command_id=q.command_id WHERE c.generation_id=? AND q.outcome='pending'`, binding.GenerationID)
	if err != nil {
		return err
	}
	type missing struct {
		id   string
		turn int64
	}
	var unresolved []missing
	for rows.Next() {
		var p missing
		if err := rows.Scan(&p.id, &p.turn); err != nil {
			rows.Close()
			return err
		}
		unresolved = append(unresolved, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range unresolved {
		if _, err := recordSteeringOutcomeTx(ctx, tx, binding.SessionID, binding.GenerationID, p.turn, execution.SteeringOutcome{CommandID: p.id, Status: "unknown"}); err != nil {
			return err
		}
	}
	// A leased command may have reached the guest before its ACK was lost. Do
	// not requeue either leased or running work and promise exactly-once effects.
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='failed',completed_at=?,completed_by_generation=?,error_class='execution_interrupted',error=?,lease_owner=NULL,lease_expires_at=NULL WHERE session_id=? AND generation_id=? AND status IN ('leased','running')`, formatTime(now), binding.GenerationID, reason, binding.SessionID, binding.GenerationID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET active_generation_id=NULL,status=CASE WHEN status IN ('failed','destroyed') THEN status WHEN EXISTS(SELECT 1 FROM turns WHERE session_id=sessions.id AND `+pendingTurnPredicate+`) THEN 'running_active' ELSE 'running_idle' END,updated_at=? WHERE id=? AND active_generation_id=?`, formatTime(now), binding.SessionID, binding.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "interrupted recovery session CAS failed"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_recoveries SET stage='complete',updated_at=? WHERE generation_id=?`, formatTime(now), binding.GenerationID); err != nil {
		return err
	}
	_, err = appendEventTx(ctx, tx, AppendEventParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, DedupeKey: "interrupted-recovery:" + binding.GenerationID, Type: "generation.error", Payload: map[string]any{"error_class": "execution_interrupted", "error": reason, "terminal": false, "retained": true, "recovered": true}, Now: now})
	return err
}
