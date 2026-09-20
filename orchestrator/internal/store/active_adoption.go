package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

func (s *Store) BeginActiveExecutionAdoption(ctx context.Context, generation, owner string, ttl time.Duration) (execution.ExecutionAdoption, error) {
	var result execution.ExecutionAdoption
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
	if err := assertSessionNotRetiringTx(ctx, tx, b.SessionID); err != nil {
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
	if active != generation || (status != "active" && status != "idle") {
		return result, execution.ErrStaleOperation
	}
	var turn int64
	var attempt int
	var markerState string
	err = tx.QueryRowContext(ctx, `SELECT turn_id,attempt,state FROM active_execution_adoptions WHERE generation_id=?`, generation).Scan(&turn, &attempt, &markerState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	retry := b.State == "starting" && markerState == "adopting"
	if b.State == "starting" && !retry {
		return result, execution.ErrBusy
	}
	if !retry {
		if b.State != "accepted" || status != "active" {
			return result, execution.ErrBusy
		}
		if err := tx.QueryRowContext(ctx, `SELECT id,attempt FROM turns WHERE session_id=? AND generation_id=? AND lease_owner=? AND status IN ('leased','running')`, b.SessionID, generation, b.Owner).Scan(&turn, &attempt); err != nil {
			return result, err
		}
	}
	var original int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE id=? AND session_id=? AND generation_id=? AND attempt=? AND (status IN ('leased','running') OR (?=1 AND completed_by_generation=? AND status IN ('completed','failed','canceled')))`, turn, b.SessionID, generation, attempt, retry, generation).Scan(&original); err != nil {
		return result, err
	}
	if original != 1 {
		return result, execution.ErrStaleOperation
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM turns WHERE session_id=? AND id!=? AND status IN ('leased','running')) +
 (SELECT count(*) FROM environment_operations WHERE environment_id=? AND state IN ('pending','issued','uncertain')) +
 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')) +
 (SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete') +
 (SELECT count(*) FROM native_capture_activations WHERE session_id=? AND stage IN ('resuming','reading','pausing'))`, b.SessionID, turn, e.ID, b.SessionID, e.ID, b.SessionID).Scan(&busy); err != nil {
		return result, err
	}
	if busy != 0 {
		return result, execution.ErrBusy
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT event_sequence FROM environment_reconcile_requests WHERE environment_id=?),-1)`, e.ID).Scan(&result.EventSequence); err != nil {
		return result, err
	}
	// A retry, including a replacement product owner, preserves the same grant
	// revision. A newly accepted execution entering adoption gets a fresh one.
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_execution_adoptions(generation_id,turn_id,attempt,previous_owner,owner,revision,event_sequence,state) VALUES(?,?,?,?,?,1,?,'adopting') ON CONFLICT(generation_id) DO UPDATE SET turn_id=excluded.turn_id,attempt=excluded.attempt,previous_owner=CASE WHEN active_execution_adoptions.state='adopting' THEN active_execution_adoptions.previous_owner ELSE excluded.previous_owner END,owner=excluded.owner,revision=active_execution_adoptions.revision+CASE WHEN active_execution_adoptions.state='adopting' THEN 0 ELSE 1 END,event_sequence=excluded.event_sequence,state='adopting'`, generation, turn, attempt, b.Owner, owner, result.EventSequence); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE execution_bindings SET state='starting',owner=? WHERE generation_id=?`, owner, generation); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_generations SET lease_owner=?,lease_expires_at=?,last_seen_at=? WHERE generation_id=?`, owner, formatTime(now.Add(ttl)), formatTime(now), generation); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET lease_owner=?,lease_expires_at=? WHERE id=? AND generation_id=? AND attempt=? AND status IN ('leased','running')`, owner, formatTime(now.Add(ttl)), turn, generation, attempt); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_id FROM egress_grants WHERE generation_id=? ORDER BY grant_id`, generation)
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
	b.Owner, b.State = owner, "starting"
	result.Binding = b
	return result, tx.Commit()
}

func (s *Store) VerifyActiveExecutionReplay(ctx context.Context, b execution.Binding, kind string, turn *int64, payload json.RawMessage) (bool, error) {
	if turn == nil {
		return true, fmt.Errorf("active replay requires original turn")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	if err := deferredRequestScopeTx(ctx, tx, b, false); err != nil {
		return true, err
	}
	if err := executionIngressTx(ctx, tx, b.SessionID, b.GenerationID, b.Owner, time.Now().UTC()); err != nil {
		return true, err
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM turns WHERE id=? AND session_id=? AND generation_id=?`, *turn, b.SessionID, b.GenerationID).Scan(&state); err != nil {
		return true, err
	}
	if state == "leased" || state == "running" || kind == "turn_control_result" {
		return false, tx.Commit()
	}
	if err := verifyTerminalExecutionRecordTx(ctx, tx, b, kind, turn, payload); err != nil {
		return true, err
	}
	return true, tx.Commit()
}
