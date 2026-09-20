package store

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

// AdoptStartingExecution transfers only unaccepted startup. It cannot claim a
// live turn or reinterpret an accepted execution as a fresh process.
func (s *Store) AdoptStartingExecution(ctx context.Context, generation, owner string, ttl time.Duration) (execution.Binding, error) {
	var empty execution.Binding
	if ttl <= 0 {
		return empty, fmt.Errorf("positive startup lease required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return empty, err
	}
	binding, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return empty, err
	}
	if err := assertSessionNotRetiringTx(ctx, tx, binding.SessionID); err != nil {
		return empty, err
	}
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings b
 JOIN sandbox_environments e ON e.environment_id=b.environment_id
 JOIN runtime_generations g ON g.generation_id=b.generation_id
 JOIN sessions s ON s.active_generation_id=b.generation_id
 JOIN environment_operations o ON o.operation_id='start:' || b.generation_id
 WHERE b.generation_id=? AND b.state='starting' AND g.status='starting'
 AND b.store_id=e.store_id AND b.sandbox_id=e.sandbox_id AND b.epoch=e.epoch AND b.environment_fence=e.fence AND o.fence=e.fence AND o.kind='start_process' AND o.state!='failed'
 AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.generation_id=b.generation_id)
 AND NOT EXISTS(SELECT 1 FROM idle_execution_adoptions a WHERE a.generation_id=b.generation_id)
 AND NOT EXISTS(SELECT 1 FROM active_execution_adoptions a WHERE a.generation_id=b.generation_id)
 AND NOT EXISTS(SELECT 1 FROM execution_recoveries r WHERE r.generation_id=b.generation_id AND r.stage!='complete')
 AND NOT EXISTS(SELECT 1 FROM engine_backend_blocks x WHERE x.store_id=b.store_id)`, generation).Scan(&valid)
	if err != nil {
		return empty, err
	}
	if valid != 1 {
		return empty, execution.ErrStaleOperation
	}
	if _, err = tx.ExecContext(ctx, `UPDATE execution_bindings SET owner=? WHERE generation_id=?`, owner, generation); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_generations SET lease_owner=?,lease_expires_at=?,last_seen_at=? WHERE generation_id=?`, owner, formatTime(time.Now().UTC().Add(ttl)), formatTime(time.Now().UTC()), generation); err != nil {
		return empty, err
	}
	binding.Owner = owner
	return binding, tx.Commit()
}

// RecordStartingProcessExit preserves terminal stream evidence separately from
// the immutable start result. Recovery can then pause/replay/retire this writer.
func (s *Store) RecordStartingProcessExit(ctx context.Context, generation, owner string, reference sandbox.ProcessReference, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return err
	}
	b, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return err
	}
	e, err := environmentWith(ctx, tx, b.EnvironmentID)
	if err != nil {
		return err
	}
	op, err := operationWith(ctx, tx, "start:"+generation)
	if err != nil {
		return err
	}
	var observed sandbox.ProcessReference
	if err := json.Unmarshal(op.Result, &observed); err != nil {
		return err
	}
	if op.State != execution.OperationSucceeded || observed != reference || reason == "" || b.State != "starting" || b.Owner != owner || b.StoreID != reference.StoreID || b.SandboxID != reference.SandboxID || b.Epoch != reference.Epoch || b.EnvironmentFence != e.Fence || b.Epoch != e.Epoch || reference.Handle == "" {
		return execution.ErrStaleOperation
	}
	var turns int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE generation_id=?`, generation).Scan(&turns); err != nil {
		return err
	}
	if turns != 0 {
		return execution.ErrBusy
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_bindings SET process_handle=? WHERE generation_id=?`, reference.Handle, generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_start_failures(generation_id,process_handle,epoch,reason,created_at) VALUES(?,?,?,?,?) ON CONFLICT(generation_id) DO NOTHING`, generation, reference.Handle, reference.Epoch, reason, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit()
}
