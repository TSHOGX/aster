package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"harness-platform/orchestrator/internal/execution"
)

func (s *Store) GetStorageBinding(ctx context.Context, session, purpose string) (execution.StorageBinding, bool, error) {
	var b execution.StorageBinding
	err := s.db.QueryRowContext(ctx, `SELECT session_id,environment_id,purpose,store_id,volume_id,capacity FROM session_storage_bindings WHERE session_id=? AND purpose=?`, session, purpose).Scan(&b.SessionID, &b.EnvironmentID, &b.Purpose, &b.StoreID, &b.VolumeID, &b.Capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return b, false, nil
	}
	return b, err == nil, err
}
func (s *Store) RecordStorageBinding(ctx context.Context, b execution.StorageBinding) error {
	if b.SessionID == "" || b.EnvironmentID == "" || b.StoreID == "" || b.VolumeID == "" || b.Capacity <= 0 || (b.Purpose != "workspace" && b.Purpose != "bridge") {
		return fmt.Errorf("complete session volume binding required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var matches int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sandbox_environments WHERE environment_id=? AND session_id=? AND store_id=?`, b.EnvironmentID, b.SessionID, b.StoreID).Scan(&matches); err != nil {
		return err
	}
	if matches != 1 {
		return execution.ErrBackendIdentityChanged
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_storage_bindings(session_id,environment_id,purpose,store_id,volume_id,capacity) VALUES(?,?,?,?,?,?)`, b.SessionID, b.EnvironmentID, b.Purpose, b.StoreID, b.VolumeID, b.Capacity)
	if err != nil {
		return err
	}
	var environment, identity, volume string
	var capacity int64
	if err := tx.QueryRowContext(ctx, `SELECT environment_id,store_id,volume_id,capacity FROM session_storage_bindings WHERE session_id=? AND purpose=?`, b.SessionID, b.Purpose).Scan(&environment, &identity, &volume, &capacity); err != nil {
		return err
	}
	if environment != b.EnvironmentID || identity != b.StoreID || volume != b.VolumeID || capacity != b.Capacity {
		return execution.ErrOperationConflict
	}
	return tx.Commit()
}
