package store

import (
	"context"

	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"time"
)

func (s *Store) IsIdleExecutionAdopting(ctx context.Context, generation string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM idle_execution_adoptions a JOIN execution_bindings b ON b.generation_id=a.generation_id WHERE a.generation_id=? AND b.state='starting'`, generation).Scan(&count)
	return count != 0, err
}
func (s *Store) BeginIdleExecutionAdoption(ctx context.Context, generation, owner string, ttl time.Duration) (execution.IdleAdoption, error) {
	var result execution.IdleAdoption
	if ttl <= 0 {
		return result, fmt.Errorf("positive adoption lease required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return result, err
	}
	b, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return result, err
	}
	e, err := environmentWith(ctx, tx, b.EnvironmentID)
	if err != nil {
		return result, err
	}
	if b.StoreID != e.StoreID || b.SandboxID != e.SandboxID || b.Epoch != e.Epoch || b.EnvironmentFence != e.Fence || b.ProcessHandle == "" || b.State == "retired" {
		return result, execution.ErrStaleOperation
	}
	var status, active string
	if err := tx.QueryRowContext(ctx, `SELECT g.status,coalesce(s.active_generation_id,'') FROM runtime_generations g JOIN sessions s ON s.id=g.session_id WHERE g.generation_id=? AND s.status NOT IN ('failed','destroyed')`, generation).Scan(&status, &active); err != nil {
		return result, err
	}
	if active != generation {
		return result, execution.ErrStaleOperation
	}
	if b.State == "starting" {
		var adopting int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM idle_execution_adoptions WHERE generation_id=?`, generation).Scan(&adopting); err != nil {
			return result, err
		}
		if adopting != 1 || status != "starting" {
			return result, execution.ErrBusy
		}
	} else if status != "idle" {
		return result, execution.ErrBusy
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM turns WHERE session_id=? AND status IN ('leased','running')) +
 (SELECT count(*) FROM environment_operations WHERE environment_id=? AND state IN ('pending','issued','uncertain')) +
 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')) +
 (SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete') +
 (SELECT count(*) FROM native_capture_activations WHERE session_id=? AND stage IN ('resuming','reading','pausing')) +
 (SELECT count(*) FROM bridge_ingress i LEFT JOIN bridge_projections p ON p.generation_id=i.generation_id AND p.turn_id=i.turn_id AND p.attempt=i.attempt WHERE i.generation_id=? AND i.id>coalesce(p.last_record_id,0))`, b.SessionID, e.ID, b.SessionID, e.ID, b.SessionID, generation).Scan(&busy); err != nil {
		return result, err
	}
	if busy != 0 {
		return result, execution.ErrBusy
	}
	result.EventSequence = -1
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT event_sequence FROM environment_reconcile_requests WHERE environment_id=?),-1)`, e.ID).Scan(&result.EventSequence); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idle_execution_adoptions(generation_id,owner,event_sequence) VALUES(?,?,?) ON CONFLICT(generation_id) DO UPDATE SET owner=excluded.owner,event_sequence=excluded.event_sequence`, generation, owner, result.EventSequence); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_bindings SET state='starting',owner=? WHERE generation_id=?`, owner, generation); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_generations SET status='starting',lease_owner=?,lease_expires_at=?,last_seen_at=? WHERE generation_id=?`, owner, formatTime(now.Add(ttl)), formatTime(now), generation); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_id FROM egress_grants WHERE generation_id=?`, generation)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		result.Grants = append(result.Grants, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return result, err
	}
	b.Owner = owner
	b.State = "starting"
	result.Binding = b
	return result, tx.Commit()
}

func (s *Store) VerifyIdleExecutionReplay(ctx context.Context, b execution.Binding, kind string, turn *int64, payload json.RawMessage) error {
	if turn == nil {
		return fmt.Errorf("idle terminal replay requires original turn")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(b.Owner)); err != nil {
		return err
	}
	var terminal int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns t JOIN execution_bindings b ON b.generation_id=t.generation_id JOIN idle_execution_adoptions a ON a.generation_id=b.generation_id WHERE t.id=? AND t.session_id=? AND t.generation_id=? AND t.status IN ('completed','failed','canceled') AND b.state='starting' AND a.owner=? AND b.owner=a.owner`, *turn, b.SessionID, b.GenerationID, b.Owner).Scan(&terminal); err != nil {
		return err
	}
	if terminal != 1 {
		return execution.ErrOperationConflict
	}
	if kind == "ack_turn_started" {
		return tx.Commit()
	}
	if kind == "turn_control_result" {
		var outcome execution.SteeringOutcome
		if err := json.Unmarshal(payload, &outcome); err != nil {
			return err
		}
		var prior string
		if err := tx.QueryRowContext(ctx, `SELECT q.outcome FROM execution_steering q JOIN execution_turn_commands c ON c.command_id=q.command_id WHERE c.command_id=? AND c.session_id=? AND c.generation_id=? AND c.turn_id=?`, outcome.CommandID, b.SessionID, b.GenerationID, *turn).Scan(&prior); err != nil {
			return err
		}
		if prior != outcome.Status || prior == "pending" {
			return execution.ErrOperationConflict
		}
		return tx.Commit()
	}
	p := AppendEventParams{SessionID: b.SessionID, GenerationID: b.GenerationID, TurnID: turn, Type: kind, Now: time.Now().UTC()}
	var raw []byte
	var key string
	if kind == "emit_output" {
		var output struct {
			Sequence int64           `json:"output_sequence"`
			Stream   string          `json:"stream"`
			Payload  json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(payload, &output); err != nil {
			return err
		}
		p.OutputSequence = &output.Sequence
		p.Stream = output.Stream
		raw, err = normalizedOperationJSON(output.Payload)
		key = fmt.Sprintf("output:%d", output.Sequence)
	} else if kind == "ack_turn_completed" {
		var value map[string]any
		value, err = completeTurnEventPayload(payload, CompleteTurnParams{}, false)
		if err == nil {
			raw, err = json.Marshal(value)
		}
		key = "completion"
	} else {
		return fmt.Errorf("unsupported idle terminal replay")
	}
	if err != nil {
		return err
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND record_key=?`, b.GenerationID, *turn, key).Scan(&recorded); err != nil {
		return err
	}
	if recorded != 1 {
		return fmt.Errorf("idle replay contains uncommitted historical output")
	}
	duplicate, err := appendBridgeRecordTx(ctx, tx, p, raw)
	if err != nil {
		return err
	}
	if !duplicate {
		return execution.ErrOperationConflict
	}
	return tx.Commit()
}
