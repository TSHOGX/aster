package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"harness-platform/orchestrator/internal/canonicaljson"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
)

// ReserveEgressCompilation allocates dormant tuples and stores the compiler
// output in one transaction. It performs no engine or gateway side effects.
// Addresses remain consumed for the life of the engine identity, even when the
// execution fails before creation or its routes are later revoked.
func (s *Store) ReserveEgressCompilation(ctx context.Context, owner string, p egress.AllocationRequest) (egress.Compilation, error) {
	var empty egress.Compilation
	if len(p.Policy.Endpoints) > 64 {
		return empty, fmt.Errorf("at most sixty-four endpoint allocations required")
	}
	var placeholders []netip.Addr
	for i := range p.Policy.Endpoints {
		address, err := egress.SyntheticAddress(p.Deployment.SyntheticRange, uint64(i+1))
		if err != nil {
			return empty, err
		}
		placeholders = append(placeholders, address)
	}
	trial, err := egress.AllocationBindings(p, placeholders)
	if err != nil {
		return empty, err
	}
	normalized, err := egress.Compile(p.Policy, p.Catalog, trial, p.Deployment)
	if err != nil {
		return empty, err
	}
	// The compiler normalizes effective policy. Include deployment exclusions and
	// selected listeners in the intent so retries cannot change safety boundaries.
	intent, err := canonicaljson.Marshal(struct {
		Session, Environment, Generation string
		Epoch                            uint64
		Fence                            int64
		Compilation                      egress.Compilation
		Deployment                       egress.Deployment
	}{p.SessionID, p.EnvironmentID, p.GenerationID, p.Epoch, p.EnvironmentFence, normalized, p.Deployment})
	if err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return empty, err
	}
	if err := assertSessionNotRetiringTx(ctx, tx, p.SessionID); err != nil {
		return empty, err
	}
	e, err := environmentWith(ctx, tx, p.EnvironmentID)
	if err != nil {
		return empty, err
	}
	if e.SessionID != p.SessionID || e.StoreID != p.Deployment.BackendID || e.Fence != p.EnvironmentFence {
		return empty, execution.ErrStaleOperation
	}
	var driver string
	if err := tx.QueryRowContext(ctx, `SELECT s.driver_id FROM sessions s WHERE s.id=? AND s.status NOT IN ('failed','destroyed')`, p.SessionID).Scan(&driver); err != nil {
		return empty, err
	}
	if driver != p.Policy.Driver {
		return empty, execution.ErrOperationConflict
	}
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM engine_backend_blocks WHERE store_id=?`, e.StoreID).Scan(&blocked); err != nil {
		return empty, err
	}
	if blocked != 0 {
		return empty, execution.ErrBackendIdentityChanged
	}
	var previous, bindingsJSON string
	err = tx.QueryRowContext(ctx, `SELECT intent,bindings FROM egress_allocations WHERE generation_id=?`, p.GenerationID).Scan(&previous, &bindingsJSON)
	if err == nil {
		if previous != string(intent) {
			return empty, execution.ErrOperationConflict
		}
		var bindings []egress.RuntimeBinding
		if err := json.Unmarshal([]byte(bindingsJSON), &bindings); err != nil {
			return empty, err
		}
		result, err := egress.Compile(p.Policy, p.Catalog, bindings, p.Deployment)
		if err != nil {
			return empty, err
		}
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	// Fresh allocation is planned before the next execution is ready: initial
	// create uses epoch 1; retained paused environments use their next epoch.
	expected := e.Epoch + 1
	if e.SandboxID == "" {
		expected = 1
	} else if e.ObservedState != "paused" {
		return empty, execution.ErrBusy
	}
	if p.Epoch != expected {
		return empty, execution.ErrResumeConflict
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired')+(SELECT count(*) FROM environment_operations WHERE environment_id=? AND state IN ('pending','issued','uncertain'))+(SELECT count(*) FROM runtime_generations WHERE generation_id=?)+(SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete')`, e.ID, e.ID, p.GenerationID, e.ID).Scan(&busy); err != nil {
		return empty, err
	}
	if busy != 0 {
		return empty, execution.ErrBusy
	}
	var prefix string
	var next uint64
	err = tx.QueryRowContext(ctx, `SELECT synthetic_range,next_offset FROM egress_tuple_pools WHERE store_id=?`, e.StoreID).Scan(&prefix, &next)
	if errors.Is(err, sql.ErrNoRows) {
		prefix = p.Deployment.SyntheticRange.String()
		next = 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO egress_tuple_pools(store_id,synthetic_range,next_offset) VALUES(?,?,1)`, e.StoreID, prefix); err != nil {
			return empty, err
		}
	} else if err != nil {
		return empty, err
	}
	if prefix != p.Deployment.SyntheticRange.String() {
		return empty, fmt.Errorf("engine synthetic range is already pinned")
	}
	var addresses []netip.Addr
	for len(addresses) < len(p.Policy.Endpoints) {
		address, err := egress.SyntheticAddress(p.Deployment.SyntheticRange, next)
		if err != nil {
			return empty, err
		}
		next++
		// Existing compiled rows from before managed allocation also reserve all
		// ports on their address. Never reinterpret them as available space.
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM egress_route_bindings WHERE backend_id=? AND destination LIKE ?`, e.StoreID, address.String()+":%").Scan(&used); err != nil {
			return empty, err
		}
		if used == 0 {
			addresses = append(addresses, address)
		}
	}
	bindings, err := egress.AllocationBindings(p, addresses)
	if err != nil {
		return empty, err
	}
	result, err := egress.Compile(p.Policy, p.Catalog, bindings, p.Deployment)
	if err != nil {
		return empty, err
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO egress_allocations(generation_id,session_id,environment_id,store_id,epoch,environment_fence,intent,bindings,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, p.GenerationID, p.SessionID, p.EnvironmentID, e.StoreID, p.Epoch, p.EnvironmentFence, string(intent), string(raw), formatTime(time.Now().UTC())); err != nil {
		return empty, err
	}
	if err := registerEgressCompilationTx(ctx, tx, result); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_tuple_pools SET next_offset=? WHERE store_id=?`, next, e.StoreID); err != nil {
		return empty, err
	}
	return result, tx.Commit()
}
