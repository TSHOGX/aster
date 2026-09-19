package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func (s *Store) BeginIdleExecutionRelease(ctx context.Context, generation, owner string, now time.Time) (execution.IdleRelease, error) {
	return s.beginIdleExecutionRelease(ctx, generation, owner, now, false)
}

func (s *Store) BeginIdleExecutionRecovery(ctx context.Context, generation, owner string, now time.Time) (execution.IdleRelease, error) {
	return s.beginIdleExecutionRelease(ctx, generation, owner, now, true)
}

func (s *Store) beginIdleExecutionRelease(ctx context.Context, generation, owner string, now time.Time, recovery bool) (execution.IdleRelease, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var release execution.IdleRelease
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return release, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return release, err
	}
	binding, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return release, err
	}
	environment, err := environmentWith(ctx, tx, binding.EnvironmentID)
	if err != nil {
		return release, err
	}
	release.Binding = binding
	operationID := "idle-pause:" + generation
	operation, err := operationWith(ctx, tx, operationID)
	if err == nil {
		// Retry under the current durable product owner, preserving the original
		// engine revision even after its successful pause changed the binding.
		if operation.Kind != "pause" || operation.EnvironmentID != binding.EnvironmentID || operation.Fence != environment.Fence || operation.IdempotencyKey != operationID {
			return release, execution.ErrStaleOperation
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		turnPredicate := pendingTurnPredicate
		if recovery {
			// The current product owner can fence an abandoned idle binding even
			// if its old lease expired. It cannot borrow old turn authority.
			var current int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings b JOIN sandbox_environments e ON e.environment_id=b.environment_id JOIN runtime_generations g ON g.generation_id=b.generation_id JOIN sessions s ON s.id=b.session_id
 WHERE b.generation_id=? AND b.state='accepted' AND b.process_handle!='' AND b.store_id=e.store_id AND b.sandbox_id=e.sandbox_id AND b.epoch=e.epoch AND b.environment_fence=e.fence AND g.lease_owner=b.owner AND g.status='idle' AND s.active_generation_id=b.generation_id AND s.status IN ('running_idle','running_active')`, generation).Scan(&current); err != nil {
				return release, err
			}
			if current != 1 {
				return release, execution.ErrStaleOperation
			}
			turnPredicate = "status IN ('leased','running')"
		} else {
			if err := acceptedExecutionTx(ctx, tx, binding.SessionID, generation, owner, now); err != nil {
				return release, err
			}
		}
		var busy int
		if err := tx.QueryRowContext(ctx, `SELECT
		 (SELECT count(*) FROM turns WHERE session_id=? AND `+turnPredicate+`) +
		 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')) +
		 (SELECT count(*) FROM bridge_ingress i LEFT JOIN bridge_projections p ON p.generation_id=i.generation_id AND p.turn_id=i.turn_id AND p.attempt=i.attempt WHERE i.generation_id=? AND i.id>coalesce(p.last_record_id,0))`, binding.SessionID, binding.SessionID, generation).Scan(&busy); err != nil {
			return release, err
		}
		if busy != 0 {
			return release, execution.ErrBusy
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM runtime_generations WHERE generation_id=?`, generation).Scan(&status); err != nil {
			return release, err
		}
		if status != "idle" {
			return release, execution.ErrBusy
		}
		raw, err := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision}})
		if err != nil {
			return release, err
		}
		raw, err = normalizedOperationJSON(raw)
		if err != nil {
			return release, err
		}
		operation, err = beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: operationID, EnvironmentID: environment.ID, Kind: "pause", IdempotencyKey: operationID, ExpectedFence: environment.Fence, Request: raw, Now: now}, raw)
		if err != nil {
			return release, err
		}
	} else {
		return release, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT binding_id FROM egress_route_bindings WHERE generation_id=? ORDER BY binding_id`, generation)
	if err != nil {
		return release, err
	}
	for rows.Next() {
		var route string
		if err := rows.Scan(&route); err != nil {
			rows.Close()
			return release, err
		}
		release.Routes = append(release.Routes, route)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return release, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_route_bindings SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return release, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return release, err
	}
	release.Operation = operation
	return release, tx.Commit()
}

func (s *Store) FinishIdleExecutionRelease(ctx context.Context, generation, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return err
	}
	binding, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return err
	}
	environment, err := environmentWith(ctx, tx, binding.EnvironmentID)
	if err != nil {
		return err
	}
	operation, err := operationWith(ctx, tx, "idle-pause:"+generation)
	if err != nil {
		return err
	}
	var observed sandbox.Observation
	if err := json.Unmarshal(operation.Result, &observed); err != nil {
		return err
	}
	if operation.State != execution.OperationSucceeded || operation.Kind != "pause" || operation.EnvironmentID != environment.ID || operation.Fence != environment.Fence || observed.StoreID != binding.StoreID || observed.SandboxID != binding.SandboxID || observed.Epoch != binding.Epoch || observed.Epoch != environment.Epoch || observed.MutationRevision != environment.MutationRevision || observed.State != "paused" || observed.PlacementState != "lost" {
		return fmt.Errorf("idle release requires verified pause under the reserved fence")
	}
	if binding.State == "retired" {
		return tx.Commit()
	}
	if binding.State != "accepted" || binding.EnvironmentFence+1 != operation.Fence {
		return execution.ErrStaleOperation
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_bindings SET state='retired' WHERE generation_id=?`, generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_generations SET status='destroyed',ended_at=?,last_seen_at=?,lease_owner=NULL,lease_expires_at=NULL WHERE generation_id=?`, formatTime(now), formatTime(now), generation); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET active_generation_id=NULL,status=CASE WHEN EXISTS(SELECT 1 FROM turns WHERE session_id=sessions.id AND `+pendingTurnPredicate+`) THEN 'running_active' ELSE 'running_idle' END,updated_at=? WHERE id=? AND active_generation_id=?`, formatTime(now), binding.SessionID, generation)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "idle release session CAS failed"); err != nil {
		return err
	}
	if _, err := appendEventTx(ctx, tx, AppendEventParams{SessionID: binding.SessionID, GenerationID: generation, Type: "generation.paused", DedupeKey: "idle-pause:" + generation, Payload: map[string]any{"epoch": binding.Epoch, "retained": true}, Now: now}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginRetainedExecutionResume(ctx context.Context, generation, owner string, now time.Time) (execution.Operation, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Operation{}, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return execution.Operation{}, err
	}
	binding, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return execution.Operation{}, err
	}
	environment, err := environmentWith(ctx, tx, binding.EnvironmentID)
	if err != nil {
		return execution.Operation{}, err
	}
	if binding.State != "retired" {
		return execution.Operation{}, execution.ErrBusy
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
	 (SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired') +
	 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain'))`, environment.ID, binding.SessionID).Scan(&busy); err != nil {
		return execution.Operation{}, err
	}
	if busy != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	operationID := "retained-resume:" + generation
	operation, err := operationWith(ctx, tx, operationID)
	if err == nil {
		if operation.EnvironmentID != environment.ID || operation.Kind != "resume" || operation.IdempotencyKey != operationID || operation.Fence != environment.Fence {
			return execution.Operation{}, execution.ErrStaleOperation
		}
		return operation, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return execution.Operation{}, err
	}
	pause, err := operationWith(ctx, tx, "idle-pause:"+generation)
	if errors.Is(err, sql.ErrNoRows) {
		var stage string
		if findErr := tx.QueryRowContext(ctx, `SELECT stage FROM execution_recoveries WHERE generation_id=?`, generation).Scan(&stage); findErr != nil {
			return execution.Operation{}, findErr
		}
		if stage != "complete" {
			return execution.Operation{}, execution.ErrBusy
		}
		pause, err = operationWith(ctx, tx, "interrupt-final-pause:"+generation)
	}
	if err != nil {
		return execution.Operation{}, err
	}
	if pause.Fence != environment.Fence {
		var maintenancePause string
		if err := tx.QueryRowContext(ctx, `SELECT last_pause_operation_id FROM native_capture_activations WHERE environment_id=? AND stage IN ('complete','requested') AND last_pause_operation_id!=''`, environment.ID).Scan(&maintenancePause); err != nil {
			return execution.Operation{}, err
		}
		pause, err = operationWith(ctx, tx, maintenancePause)
		if err != nil {
			return execution.Operation{}, err
		}
	}
	var observed sandbox.Observation
	if err := json.Unmarshal(pause.Result, &observed); err != nil {
		return execution.Operation{}, err
	}
	if pause.State != execution.OperationSucceeded || pause.Kind != "pause" || pause.EnvironmentID != environment.ID || pause.Fence != environment.Fence || observed.State != "paused" || observed.PlacementState != "lost" || observed.StoreID != environment.StoreID || observed.SandboxID != environment.SandboxID || observed.Epoch != environment.Epoch || observed.MutationRevision != environment.MutationRevision {
		return execution.Operation{}, execution.ErrStaleOperation
	}
	raw, err := json.Marshal(execution.EnvironmentRequest{Reference: observed.Reference})
	if err != nil {
		return execution.Operation{}, err
	}
	raw, err = normalizedOperationJSON(raw)
	if err != nil {
		return execution.Operation{}, err
	}
	operation, err = beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: operationID, EnvironmentID: environment.ID, Kind: "resume", IdempotencyKey: operationID, ExpectedFence: environment.Fence, Request: raw, Now: now}, raw)
	if err != nil {
		return execution.Operation{}, err
	}
	return operation, tx.Commit()
}
