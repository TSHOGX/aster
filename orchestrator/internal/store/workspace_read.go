package store

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/workspace"
	"time"
)

func (s *Store) BeginOfflineWorkspaceRead(ctx context.Context, session, owner, id string) (workspace.ReadLease, error) {
	var lease workspace.ReadLease
	if session == "" || owner == "" || id == "" {
		return lease, fmt.Errorf("offline workspace read identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return lease, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return lease, err
	}
	if err := assertSessionNotRetiringTx(ctx, tx, session); err != nil {
		return lease, err
	}
	var environment, state string
	if err := tx.QueryRowContext(ctx, `SELECT e.environment_id,e.observed_state,e.store_id,e.sandbox_id,e.epoch,e.mutation_revision,v.volume_id FROM sandbox_environments e JOIN session_storage_bindings v ON v.environment_id=e.environment_id AND v.session_id=e.session_id AND v.store_id=e.store_id AND v.purpose='workspace' WHERE e.session_id=?`, session).Scan(&environment, &state, &lease.Reference.StoreID, &lease.Reference.SandboxID, &lease.Reference.Epoch, &lease.Reference.MutationRevision, &lease.Volume.VolumeID); err != nil {
		return lease, err
	}
	if state != "paused" {
		return lease, workspace.ErrMutationBusy
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM environment_operations WHERE environment_id=? AND state IN ('pending','issued','uncertain')) +
 (SELECT count(*) FROM workspace_mutations WHERE session_id=? AND state IN ('active','uncertain')) +
 (SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired') +
 (SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete') +
 (SELECT count(*) FROM native_capture_activations WHERE environment_id=? AND stage IN ('resuming','reading','pausing'))`, environment, session, environment, environment, environment).Scan(&busy); err != nil {
		return lease, err
	}
	if busy != 0 {
		return lease, workspace.ErrMutationBusy
	}
	lease.ID = id
	lease.SessionID = session
	lease.Owner = owner
	lease.Volume.StoreID = lease.Reference.StoreID
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_read_leases(lease_id,session_id,environment_id,owner,epoch,mutation_revision,volume_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, session, environment, owner, lease.Reference.Epoch, lease.Reference.MutationRevision, lease.Volume.VolumeID, formatTime(time.Now().UTC()))
	if err != nil {
		return lease, err
	}
	return lease, tx.Commit()
}
func (s *Store) EndOfflineWorkspaceRead(ctx context.Context, p workspace.ReadLease) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_read_leases WHERE lease_id=? AND session_id=? AND owner=? AND epoch=? AND mutation_revision=? AND volume_id=?`, p.ID, p.SessionID, p.Owner, p.Reference.Epoch, p.Reference.MutationRevision, p.Volume.VolumeID)
	return err
}

// ReconcileOfflineWorkspaceReaders is called after acquiring the exclusive
// product process lock. Only readers belonging to a replaced owner are released;
// elapsed time is never evidence that a live download stopped.
func (s *Store) ReconcileOfflineWorkspaceReaders(ctx context.Context, owner string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_read_leases WHERE owner!=?`, owner); err != nil {
		return err
	}
	return tx.Commit()
}
