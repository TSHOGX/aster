package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

func assertSessionNotRetiringTx(ctx context.Context, tx *sql.Tx, session string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_retirements WHERE session_id=?`, session).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("session cannot accept input: retirement requested")
	}
	return nil
}
func (s *Store) AssertSessionNotRetiring(ctx context.Context, session string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertSessionNotRetiringTx(ctx, tx, session); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) BeginSessionRetirement(ctx context.Context, session, owner string) (execution.SessionRetirement, error) {
	result := execution.SessionRetirement{SessionID: session}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return result, err
	}
	var backend string
	if err := tx.QueryRowContext(ctx, `SELECT backend FROM session_execution_backends WHERE session_id=?`, session).Scan(&backend); err != nil {
		return result, err
	}
	if backend != "khora" {
		return result, execution.ErrOperationConflict
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_retirements(session_id,state,created_at) VALUES(?,'requested',?)`, session, now); err != nil {
		return result, err
	}
	// Park pending inputs. Steering outcomes still need their queued source rows
	// until replay settles delivery, so cancellation happens only at finalization.
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET auto_dispatch=0 WHERE session_id=? AND status='queued'`, session); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id IN (SELECT generation_id FROM execution_bindings WHERE session_id=?)`, session); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_route_bindings SET state='revoked' WHERE generation_id IN (SELECT generation_id FROM execution_bindings WHERE session_id=?)`, session); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func retirementWith(ctx context.Context, tx *sql.Tx, session, owner string) (execution.SessionRetirement, error) {
	p := execution.SessionRetirement{SessionID: session}
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return p, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT state,coalesce(event_id,0) FROM session_retirements WHERE session_id=?`, session).Scan(&p.Action, &p.EventID); err != nil {
		return p, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.binding_id FROM egress_route_bindings r JOIN execution_bindings b ON b.generation_id=r.generation_id WHERE b.session_id=? ORDER BY r.binding_id`, session)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var route string
		if err := rows.Scan(&route); err != nil {
			rows.Close()
			return p, err
		}
		p.Routes = append(p.Routes, route)
	}
	err = rows.Err()
	rows.Close()
	return p, err
}
func (s *Store) NextSessionRetirement(ctx context.Context, session, owner string) (execution.SessionRetirement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.SessionRetirement{}, err
	}
	defer tx.Rollback()
	p, err := retirementWith(ctx, tx, session, owner)
	if err != nil {
		return p, err
	}
	if p.Action == "complete" {
		return p, tx.Commit()
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain'))+(SELECT count(*) FROM native_capture_activations WHERE session_id=? AND stage IN ('resuming','reading','pausing'))`, session, session).Scan(&busy); err != nil {
		return p, err
	}
	if busy != 0 {
		return p, execution.ErrBusy
	}
	var generation string
	err = tx.QueryRowContext(ctx, `SELECT r.generation_id,r.reason FROM execution_recoveries r JOIN execution_bindings b ON b.generation_id=r.generation_id WHERE b.session_id=? AND r.stage!='complete'`, session).Scan(&generation, &p.RecoveryReason)
	if err == nil {
		p.Action, p.GenerationID = "recover", generation
		return p, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	err = tx.QueryRowContext(ctx, `SELECT b.generation_id FROM execution_bindings b JOIN sessions s ON s.active_generation_id=b.generation_id WHERE b.session_id=? AND b.state!='retired'`, session).Scan(&generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if err == nil {
		b, err := bindingWith(ctx, tx, generation)
		if err != nil {
			return p, err
		}
		p.GenerationID = generation
		// A pause already dispatched must finish on its original conditional lane.
		var release int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_operations WHERE operation_id=?`, "idle-pause:"+generation).Scan(&release); err != nil {
			return p, err
		}
		if release != 0 {
			p.Action = "release"
			return p, tx.Commit()
		}
		var inflight, stream int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM turns WHERE generation_id=? AND status IN ('leased','running')),(SELECT count(*) FROM bridge_streams WHERE generation_id=? AND backend_id=?)`, generation, generation, b.StoreID).Scan(&inflight, &stream); err != nil {
			return p, err
		}
		if inflight != 0 || stream != 0 {
			p.Action, p.RecoveryReason = "recover", execution.SessionRetirementReason
			return p, tx.Commit()
		}
	}
	var environmentID string
	err = tx.QueryRowContext(ctx, `SELECT environment_id FROM sandbox_environments WHERE session_id=?`, session).Scan(&environmentID)
	if errors.Is(err, sql.ErrNoRows) {
		p.Action = "finish"
		return p, tx.Commit()
	}
	if err != nil {
		return p, err
	}
	e, err := environmentWith(ctx, tx, environmentID)
	if err != nil {
		return p, err
	}
	if e.SandboxID == "" {
		return p, execution.ErrBusy
	}
	key := "retire-pause:" + session
	operation, err := operationWith(ctx, tx, key)
	if err == nil {
		if operation.EnvironmentID != e.ID || operation.Fence != e.Fence {
			return p, execution.ErrStaleOperation
		}
		if operation.State == execution.OperationSucceeded {
			p.Action = "finish"
			return p, tx.Commit()
		}
		p.Action, p.Operation = "pause", operation
		return p, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	// Even an already paused environment is conditionally paused/inspected to
	// obtain writer-termination evidence under this retirement's own fence.
	raw, _ := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: e.StoreID, SandboxID: e.SandboxID, Epoch: e.Epoch, MutationRevision: e.MutationRevision}})
	raw, err = normalizedOperationJSON(raw)
	if err != nil {
		return p, err
	}
	op, err := beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: key, EnvironmentID: e.ID, Kind: "pause", IdempotencyKey: key, ExpectedFence: e.Fence, Request: raw, Now: time.Now().UTC()}, raw)
	if err != nil {
		return p, err
	}
	p.Action, p.Operation = "pause", op
	return p, tx.Commit()
}
func (s *Store) FinishSessionRetirement(ctx context.Context, session, owner string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	p, err := retirementWith(ctx, tx, session, owner)
	if err != nil {
		return 0, err
	}
	if p.Action == "complete" {
		return p.EventID, tx.Commit()
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM turns WHERE session_id=? AND status IN ('leased','running'))+
 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain'))+
 (SELECT count(*) FROM workspace_read_leases WHERE session_id=?)+
 (SELECT count(*) FROM native_capture_activations WHERE session_id=? AND stage IN ('resuming','reading','pausing'))+
 (SELECT count(*) FROM execution_recoveries r JOIN execution_bindings b ON b.generation_id=r.generation_id WHERE b.session_id=? AND r.stage!='complete')+
 (SELECT count(*) FROM environment_operations o JOIN sandbox_environments e ON e.environment_id=o.environment_id WHERE e.session_id=? AND o.state IN ('pending','issued','uncertain'))+
 (SELECT count(*) FROM bridge_ingress i LEFT JOIN bridge_projections p ON p.generation_id=i.generation_id AND p.turn_id=i.turn_id AND p.attempt=i.attempt WHERE i.session_id=? AND i.id>coalesce(p.last_record_id,0))`, session, session, session, session, session, session, session).Scan(&busy); err != nil {
		return 0, err
	}
	if busy != 0 {
		return 0, execution.ErrBusy
	}
	var environmentID string
	err = tx.QueryRowContext(ctx, `SELECT environment_id FROM sandbox_environments WHERE session_id=?`, session).Scan(&environmentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		e, err := environmentWith(ctx, tx, environmentID)
		if err != nil {
			return 0, err
		}
		op, err := operationWith(ctx, tx, "retire-pause:"+session)
		if err != nil {
			return 0, err
		}
		if op.Fence != e.Fence || op.State != execution.OperationSucceeded {
			return 0, execution.ErrStaleOperation
		}
		if err := verifiedRecoveryLifecycle(op, e, "paused", e.Epoch); err != nil {
			return 0, err
		}
	}
	now := time.Now().UTC()
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='canceled',completed_at=?,completed_by_generation=coalesce(completed_by_generation,generation_id),error_class='session_destroyed',error='session_destroyed',lease_owner=NULL,lease_expires_at=NULL WHERE session_id=? AND status IN ('queued','leased','running')`, stamp, session); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_bindings SET state='retired' WHERE session_id=?`, session); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_generations SET status=CASE WHEN status='failed' THEN status ELSE 'destroyed' END,ended_at=coalesce(ended_at,?),lease_owner=NULL,lease_expires_at=NULL WHERE session_id=?`, stamp, session); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET status='destroyed',active_generation_id=NULL,error_class='session_destroyed',failure_reason='session_destroyed',updated_at=?,ended_at=coalesce(ended_at,?) WHERE id=?`, stamp, stamp, session); err != nil {
		return 0, err
	}
	id, err := appendEventTx(ctx, tx, AppendEventParams{SessionID: session, DedupeKey: "session_destroyed:" + session, Type: "session.destroyed", Payload: map[string]any{"terminal": true, "status": "destroyed", "session_status": "destroyed", "session_updated_at": stamp, "updated_at": stamp, "retained": true}, Now: now})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session_retirements SET state='complete',event_id=? WHERE session_id=?`, id, session); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
