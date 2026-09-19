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
)

func (s *Store) ReserveExecutionBinding(ctx context.Context, p execution.ReserveBindingParams) (execution.Binding, error) {
	if p.GenerationID == "" || p.SessionID == "" || p.EnvironmentID == "" || p.Owner == "" || p.LeaseTTL <= 0 || !strings.HasPrefix(p.SpecDigest, "sha256:") || !strings.HasPrefix(p.ManifestDigest, "sha256:") {
		return execution.Binding{}, fmt.Errorf("generation identity, lease and release digests required")
	}
	if p.Driver != "pi" && p.Driver != "sh" {
		return execution.Binding{}, fmt.Errorf("unsupported execution driver")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Binding{}, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return execution.Binding{}, err
	}
	environment, err := environmentWith(ctx, tx, p.EnvironmentID)
	if err != nil {
		return execution.Binding{}, err
	}
	if environment.SessionID != p.SessionID || environment.SandboxID == "" || environment.Epoch == 0 {
		return execution.Binding{}, fmt.Errorf("observed environment binding required")
	}
	var readers int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_read_leases WHERE environment_id=?`, environment.ID).Scan(&readers); err != nil {
		return execution.Binding{}, err
	}
	if readers != 0 {
		return execution.Binding{}, execution.ErrBusy
	}
	var recovering int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete'`, environment.ID).Scan(&recovering); err != nil {
		return execution.Binding{}, err
	}
	if recovering != 0 {
		return execution.Binding{}, execution.ErrBusy
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM native_capture_activations WHERE environment_id=? AND stage IN ('resuming','reading','pausing')`, environment.ID).Scan(&recovering); err != nil {
		return execution.Binding{}, err
	}
	if recovering != 0 {
		return execution.Binding{}, execution.ErrBusy
	}
	if existing, err := bindingWith(ctx, tx, p.GenerationID); err == nil {
		if existing.SessionID != p.SessionID || existing.EnvironmentID != p.EnvironmentID || existing.Owner != p.Owner || existing.Driver != p.Driver || existing.SpecDigest != p.SpecDigest || existing.ManifestDigest != p.ManifestDigest || existing.EnvironmentFence != environment.Fence || existing.Epoch != environment.Epoch || existing.State != "starting" {
			return execution.Binding{}, execution.ErrOperationConflict
		}
		return existing, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return execution.Binding{}, err
	}
	var driver string
	var active sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT driver_id,active_generation_id FROM sessions WHERE id=? AND status NOT IN ('destroyed','failed')`, p.SessionID).Scan(&driver, &active); err != nil {
		return execution.Binding{}, err
	}
	if driver != p.Driver {
		return execution.Binding{}, fmt.Errorf("session driver differs from execution")
	}
	if active.Valid && active.String != "" {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM runtime_generations WHERE generation_id=?`, active.String).Scan(&state); err != nil {
			return execution.Binding{}, err
		}
		if state != "failed" && state != "destroyed" {
			return execution.Binding{}, execution.ErrBusy
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_generations(generation_id,session_id,status,runsc_platform,lease_owner,lease_expires_at,started_at,last_seen_at) VALUES(?,?,'starting',NULL,?,?,?,?)`, p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now.Add(p.LeaseTTL)), formatTime(p.Now), formatTime(p.Now))
	if err != nil {
		return execution.Binding{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_bindings(generation_id,session_id,environment_id,store_id,sandbox_id,epoch,environment_fence,owner,driver_id,state,spec_digest,manifest_digest,protocol_version,turn_input_schema) VALUES(?,?,?,?,?,?,?,?,?,'starting',?,?,2,'RunTurn')`, p.GenerationID, p.SessionID, p.EnvironmentID, environment.StoreID, environment.SandboxID, environment.Epoch, environment.Fence, p.Owner, p.Driver, p.SpecDigest, p.ManifestDigest)
	if err != nil {
		return execution.Binding{}, err
	}
	var payload []byte
	if p.Driver == "pi" {
		payload, err = json.Marshal(map[string]any{"schema_version": 1, "driver_id": "pi", "state_kind": "pi_uninitialized", "session_dir": "/root/.pi/agent/sessions"})
	} else {
		payload, _, err = canonicalBootstrapDriverState(p.Driver)
	}
	if err != nil {
		return execution.Binding{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_driver_states(session_id,driver_id,state_payload,state_digest,state_version,updated_generation_id,updated_at) VALUES(?,?,?,?,1,?,?)`, p.SessionID, p.Driver, string(payload), DriverStateDigest(payload), p.GenerationID, formatTime(p.Now))
	if err != nil {
		return execution.Binding{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET active_generation_id=?,status=CASE WHEN EXISTS(SELECT 1 FROM turns WHERE session_id=sessions.id AND `+pendingTurnPredicate+`) THEN 'running_active' ELSE 'running_idle' END,updated_at=? WHERE id=?`, p.GenerationID, formatTime(p.Now), p.SessionID)
	if err != nil {
		return execution.Binding{}, err
	}
	binding, err := bindingWith(ctx, tx, p.GenerationID)
	if err != nil {
		return execution.Binding{}, err
	}
	return binding, tx.Commit()
}

func (s *Store) GetExecutionBinding(ctx context.Context, generation string) (execution.Binding, error) {
	return bindingWith(ctx, s.db, generation)
}
func bindingWith(ctx context.Context, db dbRunner, generation string) (execution.Binding, error) {
	var b execution.Binding
	err := db.QueryRowContext(ctx, `SELECT generation_id,session_id,environment_id,store_id,sandbox_id,process_handle,epoch,environment_fence,owner,driver_id,state,spec_digest,manifest_digest,protocol_version,turn_input_schema FROM execution_bindings WHERE generation_id=?`, generation).Scan(&b.GenerationID, &b.SessionID, &b.EnvironmentID, &b.StoreID, &b.SandboxID, &b.ProcessHandle, &b.Epoch, &b.EnvironmentFence, &b.Owner, &b.Driver, &b.State, &b.SpecDigest, &b.ManifestDigest, &b.ProtocolVersion, &b.TurnInputSchema)
	return b, err
}

func (s *Store) ObserveExecutionProcess(ctx context.Context, p execution.ObserveBindingParams) error {
	if p.ProcessHandle == "" || p.Epoch == 0 {
		return fmt.Errorf("observed process handle and epoch required")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE execution_bindings SET process_handle=? WHERE generation_id=? AND owner=? AND state='starting' AND epoch=? AND environment_fence=? AND (process_handle='' OR process_handle=?) AND EXISTS(SELECT 1 FROM sandbox_environments e WHERE e.environment_id=execution_bindings.environment_id AND e.fence=execution_bindings.environment_fence AND e.epoch=execution_bindings.epoch) AND EXISTS(SELECT 1 FROM runtime_generations g WHERE g.generation_id=execution_bindings.generation_id AND g.lease_owner=? AND g.lease_expires_at>?)`, p.ProcessHandle, p.GenerationID, p.Owner, p.Epoch, p.EnvironmentFence, p.ProcessHandle, p.Owner, formatTime(p.Now))
	if err != nil {
		return err
	}
	return requireOneRow(result, "execution process observation CAS failed")
}

func (s *Store) AcceptExecutionBinding(ctx context.Context, p execution.ObserveBindingParams) error {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return err
	}
	if p.ReconciledEventSequence != nil {
		if *p.ReconciledEventSequence < -1 {
			return fmt.Errorf("invalid reconciled event sequence")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM environment_reconcile_requests WHERE event_sequence<=? AND environment_id=(SELECT environment_id FROM execution_bindings WHERE generation_id=?)`, *p.ReconciledEventSequence, p.GenerationID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_bindings SET state='accepted' WHERE generation_id=? AND owner=? AND process_handle=? AND process_handle!='' AND epoch=? AND environment_fence=? AND state='starting' AND EXISTS(SELECT 1 FROM sandbox_environments e WHERE e.environment_id=execution_bindings.environment_id AND e.fence=execution_bindings.environment_fence AND e.epoch=execution_bindings.epoch) AND NOT EXISTS(SELECT 1 FROM environment_reconcile_requests r WHERE r.environment_id=execution_bindings.environment_id) AND NOT EXISTS(SELECT 1 FROM environment_operations o WHERE o.environment_id=execution_bindings.environment_id AND o.state IN ('pending','issued','uncertain'))`, p.GenerationID, p.Owner, p.ProcessHandle, p.Epoch, p.EnvironmentFence)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "execution acceptance CAS failed"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runtime_generations SET status='idle',last_seen_at=? WHERE generation_id=? AND lease_owner=? AND lease_expires_at>? AND status='starting'`, formatTime(p.Now), p.GenerationID, p.Owner, formatTime(p.Now))
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "execution ready lease CAS failed"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sandbox_environments SET observed_state='running' WHERE environment_id=(SELECT environment_id FROM execution_bindings WHERE generation_id=?)`, p.GenerationID); err != nil {
		return err
	}
	return tx.Commit()
}

// acceptedExecutionTx gates new dispatch and service authority. An engine event
// invalidates readiness until fresh process evidence has been reconciled.
func acceptedExecutionTx(ctx context.Context, tx *sql.Tx, session, generation, owner string, now time.Time) error {
	if err := executionIngressTx(ctx, tx, session, generation, owner, now); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_reconcile_requests r JOIN execution_bindings b ON b.environment_id=r.environment_id WHERE b.generation_id=?`, generation).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("execution readiness awaits engine reconciliation")
	}
	return nil
}

// executionIngressTx permits settlement from the original accepted process while
// an event is awaiting inspection. This grants no new work or egress authority.
// Owner, lease, placement, lifecycle and session fences still apply; a replaced
// execution must use the explicit historical recovery path.
func executionIngressTx(ctx context.Context, tx *sql.Tx, session, generation, owner string, now time.Time) error {
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return err
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings b JOIN sandbox_environments e ON e.environment_id=b.environment_id JOIN runtime_generations g ON g.generation_id=b.generation_id JOIN sessions s ON s.id=b.session_id WHERE b.session_id=? AND b.generation_id=? AND b.owner=? AND b.state='accepted' AND b.process_handle!='' AND b.store_id=e.store_id AND b.sandbox_id=e.sandbox_id AND b.epoch=e.epoch AND b.environment_fence=e.fence AND g.lease_owner=b.owner AND g.lease_expires_at>? AND g.status IN ('idle','active') AND s.active_generation_id=b.generation_id AND s.status NOT IN ('failed','destroyed') AND NOT EXISTS(SELECT 1 FROM environment_operations o WHERE o.environment_id=b.environment_id AND o.state IN ('pending','issued','uncertain'))`, session, generation, owner, formatTime(now)).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("execution ingress binding CAS failed")
	}
	return nil
}

func (s *Store) RenewExecutionBindingLease(ctx context.Context, generation, owner string, ttl time.Duration, now time.Time) error {
	if ttl <= 0 {
		return fmt.Errorf("positive execution lease required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runtime_generations SET lease_expires_at=?,last_seen_at=?
 WHERE generation_id=? AND lease_owner=? AND lease_expires_at>? AND status IN ('starting','idle','active')
 AND EXISTS(SELECT 1 FROM execution_bindings b JOIN sandbox_environments e ON e.environment_id=b.environment_id JOIN sessions s ON s.id=b.session_id
 WHERE b.generation_id=runtime_generations.generation_id AND b.owner=? AND b.state!='retired' AND b.environment_fence=e.fence AND b.epoch=e.epoch AND s.active_generation_id=b.generation_id AND s.status NOT IN ('failed','destroyed'))
 AND EXISTS(SELECT 1 FROM orchestrator_owner WHERE singleton=1 AND uuid=?)`, formatTime(now.Add(ttl)), formatTime(now), generation, owner, formatTime(now), owner, ownerUUIDFromLeaseOwner(owner))
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "execution lease renewal CAS failed"); err != nil {
		return fmt.Errorf("%w: %v", execution.ErrStartLeaseLost, err)
	}
	return nil
}

func (s *Store) AssertExecutionAccepted(ctx context.Context, generation, owner string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	binding, err := bindingWith(ctx, tx, generation)
	if err != nil {
		return err
	}
	if err := acceptedExecutionTx(ctx, tx, binding.SessionID, generation, owner, now); err != nil {
		return err
	}
	return tx.Commit()
}
