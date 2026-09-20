package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/nativecapture"
	"harness-platform/orchestrator/internal/sandbox"
)

func (s *Store) RequestNativeCaptureActivation(ctx context.Context, p nativecapture.Request) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var environment string
	if err := tx.QueryRowContext(ctx, `SELECT e.environment_id FROM native_capture_requests r JOIN sandbox_environments e ON e.session_id=r.session_id AND e.store_id=r.store_id AND e.sandbox_id=r.sandbox_id WHERE r.session_id=? AND r.state_version=? AND r.state_digest=? AND r.native_digest=? AND r.store_id=? AND r.sandbox_id=? AND r.epoch=?`, p.SessionID, p.StateVersion, p.StateDigest, p.NativeDigest, p.StoreID, p.SandboxID, p.Epoch).Scan(&environment); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM native_capture_requests r WHERE r.session_id=? AND r.state_version=? AND NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=r.session_id AND c.state_version=r.state_version)`, p.SessionID, p.StateVersion).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_capture_activations(session_id,environment_id,through_version,stage,updated_at)
 SELECT ?,?,max(state_version),'requested',? FROM native_capture_requests WHERE session_id=?
 ON CONFLICT(session_id) DO UPDATE SET through_version=excluded.through_version,attempt=native_capture_activations.attempt+1,stage='requested',operation_id='',deadline='',updated_at=excluded.updated_at WHERE native_capture_activations.stage='complete'`, p.SessionID, environment, formatTime(time.Now().UTC()), p.SessionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PendingCaptureActivations(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 128 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `SELECT session_id FROM native_capture_activations WHERE stage!='complete' ORDER BY updated_at,session_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var session string
		if err := rows.Scan(&session); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) NextCaptureMaintenance(ctx context.Context, session, owner string, maximum int, window time.Duration, now time.Time) (execution.CaptureMaintenance, error) {
	state := execution.CaptureMaintenance{SessionID: session}
	if maximum <= 0 || window <= 0 {
		return state, fmt.Errorf("positive maintenance admission and duration required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return state, err
	}
	var operationID, deadline string
	var through, attempt int
	if err := tx.QueryRowContext(ctx, `SELECT environment_id,stage,operation_id,deadline,through_version,attempt FROM native_capture_activations WHERE session_id=?`, session).Scan(&state.EnvironmentID, &state.Stage, &operationID, &deadline, &through, &attempt); err != nil {
		return state, err
	}
	if state.Stage == "complete" {
		return state, tx.Commit()
	}
	environment, err := environmentWith(ctx, tx, state.EnvironmentID)
	if err != nil {
		return state, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM native_capture_requests r WHERE session_id=? AND state_version<=? AND NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=r.session_id AND c.state_version=r.state_version)`, session, through).Scan(&pending); err != nil {
		return state, err
	}
	if state.Stage == "requested" {
		var retiring int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_retirements WHERE session_id=? AND state='requested'`, session).Scan(&retiring); err != nil {
			return state, err
		}
		if retiring != 0 {
			return state, execution.ErrBusy
		}
		if pending == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE native_capture_activations SET stage='complete',updated_at=? WHERE session_id=?`, formatTime(now), session); err != nil {
				return state, err
			}
			state.Stage = "complete"
			return state, tx.Commit()
		}
		var busy int
		if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM turns WHERE session_id=? AND `+pendingTurnPredicate+`) +
 (SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired') +
 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')) +
 (SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete')`, session, environment.ID, session, environment.ID).Scan(&busy); err != nil {
			return state, err
		}
		if busy != 0 || environment.ObservedState != "paused" {
			return state, execution.ErrBusy
		}
		count, err := countSandboxOccupyingSessions(ctx, tx)
		if err != nil {
			return state, err
		}
		if count >= maximum {
			return state, &sandbox.Error{Class: sandbox.Capacity, Operation: "capture admission", Cause: fmt.Errorf("active session limit reached")}
		}
		state.Stage = "resuming"
		operationID = fmt.Sprintf("capture-resume:%s:%d", session, attempt)
		deadline = formatTime(now.Add(window))
	} else {
		op, err := operationWith(ctx, tx, operationID)
		if err != nil {
			return state, err
		}
		if op.EnvironmentID != environment.ID || op.Fence != environment.Fence {
			return state, execution.ErrStaleOperation
		}
		if op.State == execution.OperationFailed && state.Stage == "resuming" {
			// A definitive activation rejection keeps the original paused binding.
			// Verify pause under a fresh intent before releasing the workflow lane.
			state.Stage = "pausing"
			operationID = fmt.Sprintf("capture-pause:%s:%d", session, attempt)
		} else {
			if op.State == execution.OperationFailed {
				return state, fmt.Errorf("capture pause requires reconciliation: %s", op.Error)
			}
			if op.State != execution.OperationSucceeded {
				state.Operation = op
				return state, tx.Commit()
			}
			switch state.Stage {
			case "resuming":
				if err := verifiedRecoveryLifecycle(op, environment, "running", environment.Epoch); err != nil {
					return state, err
				}
				state.Stage = "reading"
			case "pausing":
				if err := verifiedRecoveryLifecycle(op, environment, "paused", environment.Epoch); err != nil {
					return state, err
				}
				state.Stage = "complete"
				if _, err := tx.ExecContext(ctx, `UPDATE native_capture_activations SET last_pause_operation_id=? WHERE session_id=?`, operationID, session); err != nil {
					return state, err
				}
			case "reading":
			default:
				return state, execution.ErrOperationConflict
			}
		}
	}
	if state.Stage == "reading" {
		var work int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND `+pendingTurnPredicate, session).Scan(&work); err != nil {
			return state, err
		}
		if pending == 0 || work != 0 || !parseTime(deadline).After(now) {
			state.Stage = "pausing"
			operationID = fmt.Sprintf("capture-pause:%s:%d", session, attempt)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_capture_activations SET stage=?,operation_id=?,deadline=?,updated_at=? WHERE session_id=?`, state.Stage, operationID, deadline, formatTime(now), session); err != nil {
		return state, err
	}
	if state.Stage == "resuming" || state.Stage == "pausing" {
		kind := "resume"
		if state.Stage == "pausing" {
			kind = "pause"
		}
		raw, err := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision}})
		if err != nil {
			return state, err
		}
		raw, err = normalizedOperationJSON(raw)
		if err != nil {
			return state, err
		}
		state.Operation, err = beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: operationID, EnvironmentID: environment.ID, Kind: kind, IdempotencyKey: operationID, ExpectedFence: environment.Fence, Request: raw, Now: now}, raw)
		if err != nil {
			return state, err
		}
	}
	return state, tx.Commit()
}
