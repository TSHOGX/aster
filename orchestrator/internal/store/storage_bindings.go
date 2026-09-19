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

func (s *Store) FindSessionEnvironment(ctx context.Context, session string) (execution.Environment, bool, error) {
	environment, err := s.GetSessionEnvironment(ctx, session)
	if errors.Is(err, sql.ErrNoRows) {
		return environment, false, nil
	}
	return environment, err == nil, err
}
func (s *Store) FindActiveExecutionBinding(ctx context.Context, session string) (execution.Binding, bool, error) {
	var generation string
	err := s.db.QueryRowContext(ctx, `SELECT b.generation_id FROM execution_bindings b JOIN sessions s ON s.active_generation_id=b.generation_id WHERE b.session_id=? AND b.state!='retired'`, session).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Binding{}, false, nil
	}
	if err != nil {
		return execution.Binding{}, false, err
	}
	binding, err := s.GetExecutionBinding(ctx, generation)
	return binding, err == nil, err
}
