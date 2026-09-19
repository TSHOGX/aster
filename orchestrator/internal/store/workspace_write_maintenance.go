package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

func reserveWorkspaceLifecycleTx(ctx context.Context, tx *sql.Tx, e execution.Environment, id, kind string) (execution.Operation, error) {
	raw, err := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: e.StoreID, SandboxID: e.SandboxID, Epoch: e.Epoch, MutationRevision: e.MutationRevision}})
	if err != nil {
		return execution.Operation{}, err
	}
	raw, err = normalizedOperationJSON(raw)
	if err != nil {
		return execution.Operation{}, err
	}
	return beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: id, EnvironmentID: e.ID, Kind: kind, IdempotencyKey: id, ExpectedFence: e.Fence, Request: raw, Now: time.Now().UTC()}, raw)
}

func (s *Store) NextGuestWorkspaceMaintenance(ctx context.Context, id, owner string) (execution.WorkspaceMaintenance, error) {
	var state execution.WorkspaceMaintenance
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return state, err
	}
	var environmentID, operationID, outcome, recordedOwner string
	if err := tx.QueryRowContext(ctx, `SELECT w.environment_id,w.stage,w.operation_id,w.outcome,m.owner FROM workspace_write_maintenance w JOIN workspace_mutations m ON m.operation_id=w.mutation_id WHERE w.mutation_id=?`, id).Scan(&environmentID, &state.Stage, &operationID, &outcome, &recordedOwner); err != nil {
		return state, err
	}
	if state.Stage == "complete" {
		return state, tx.Commit()
	}
	if owner != recordedOwner {
		return state, fmt.Errorf("workspace maintenance owner changed")
	}
	environment, err := environmentWith(ctx, tx, environmentID)
	if err != nil {
		return state, err
	}
	if state.Stage == "writing" {
		return state, tx.Commit()
	}
	if state.Stage == "pausing" && operationID == "" {
		operationID = "workspace-pause:" + id
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_write_maintenance SET operation_id=? WHERE mutation_id=?`, operationID, id); err != nil {
			return state, err
		}
		state.Operation, err = reserveWorkspaceLifecycleTx(ctx, tx, environment, operationID, "pause")
		if err != nil {
			return state, err
		}
		return state, tx.Commit()
	}
	op, err := operationWith(ctx, tx, operationID)
	if err != nil {
		return state, err
	}
	if op.EnvironmentID != environmentID || op.Fence != environment.Fence {
		return state, execution.ErrStaleOperation
	}
	if op.State == execution.OperationFailed {
		if state.Stage != "resuming" {
			return state, fmt.Errorf("workspace pause requires reconciliation: %s", op.Error)
		}
		operationID = "workspace-pause:" + id
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_write_maintenance SET stage='pausing',operation_id=?,outcome='failed' WHERE mutation_id=?`, operationID, id); err != nil {
			return state, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_mutations SET error=?,updated_at=? WHERE operation_id=?`, op.Error, formatTime(time.Now().UTC()), id); err != nil {
			return state, err
		}
		state.Stage = "pausing"
		state.Operation, err = reserveWorkspaceLifecycleTx(ctx, tx, environment, operationID, "pause")
		if err != nil {
			return state, err
		}
		return state, tx.Commit()
	}
	state.Operation = op
	if op.State != execution.OperationSucceeded {
		return state, tx.Commit()
	}
	var before execution.EnvironmentRequest
	if err := json.Unmarshal(op.Request, &before); err != nil {
		return state, err
	}
	if state.Stage == "resuming" {
		if err := verifiedRecoveryLifecycle(op, environment, "running", before.Reference.Epoch+1); err != nil {
			return state, err
		}
		var observed sandbox.Observation
		if err := json.Unmarshal(op.Result, &observed); err != nil {
			return state, err
		}
		raw, err := json.Marshal(observed.Reference)
		if err != nil {
			return state, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_guest_mutations SET reference=? WHERE operation_id=? AND issued=0`, string(raw), id); err != nil {
			return state, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_write_maintenance SET stage='writing' WHERE mutation_id=?`, id); err != nil {
			return state, err
		}
		state.Stage = "writing"
	} else {
		if err := verifiedRecoveryLifecycle(op, environment, "paused", before.Reference.Epoch); err != nil {
			return state, err
		}
		if outcome != "succeeded" && outcome != "failed" {
			return state, fmt.Errorf("workspace maintenance has no settled file outcome")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_mutations SET state=?,updated_at=? WHERE operation_id=?`, outcome, formatTime(time.Now().UTC()), id); err != nil {
			return state, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_write_maintenance SET stage='complete' WHERE mutation_id=?`, id); err != nil {
			return state, err
		}
		state.Stage = "complete"
	}
	return state, tx.Commit()
}
