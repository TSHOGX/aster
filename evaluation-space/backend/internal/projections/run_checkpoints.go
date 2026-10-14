package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type RunCheckpoint struct {
	ID       string                `json:"id"`
	RunID    string                `json:"run_id"`
	Fence    int64                 `json:"fence"`
	Artifact evolution.ArtifactRef `json:"artifact"`
}

// SaveRunCheckpoint records a sealed artifact reference, not arbitrary worker
// memory. Artifact sealing and current grants must be verified by the executor.
func (s *Store) SaveRunCheckpoint(ctx context.Context, lease RunLease, value RunCheckpoint) error {
	if !manifest.ValidID(value.ID) || value.RunID != lease.RunID || value.Fence != lease.Fence || value.Artifact.Validate() != nil {
		return fmt.Errorf("invalid run checkpoint")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var group, state, worker string
	var fence, expires int64
	if err = tx.QueryRowContext(ctx, "SELECT group_id,state,worker_id,fence,lease_until FROM runs WHERE id=?", value.RunID).Scan(&group, &state, &worker, &fence, &expires); err != nil {
		return err
	}
	if state != "running" || worker != lease.WorkerID || fence != lease.Fence || expires <= time.Now().UnixNano() {
		return ErrConflict
	}
	if value.Artifact.GroupID != group {
		return fmt.Errorf("checkpoint artifact group mismatch")
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM run_checkpoints WHERE run_id=? AND id=?", value.RunID, value.ID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, raw) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_checkpoints(run_id,id,sequence,body) SELECT ?,?,coalesce(max(sequence),0)+1,? FROM run_checkpoints WHERE run_id=?", value.RunID, value.ID, raw, value.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

// LatestRunCheckpoint reads retained provenance across worker generations.
// Callers must separately authorize artifact reads before resuming from it.
func (s *Store) LatestRunCheckpoint(ctx context.Context, group, run string) (RunCheckpoint, error) {
	var result RunCheckpoint
	if !manifest.ValidID(group) || !manifest.ValidID(run) {
		return result, fmt.Errorf("invalid checkpoint scope")
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT c.body FROM run_checkpoints c JOIN runs r ON r.id=c.run_id WHERE r.group_id=? AND c.run_id=? ORDER BY c.sequence DESC LIMIT 1", group, run).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result, err = evolution.Decode[RunCheckpoint](bytes.NewReader(raw))
	if err != nil || result.RunID != run || result.Artifact.GroupID != group {
		return RunCheckpoint{}, fmt.Errorf("invalid stored checkpoint")
	}
	return result, nil
}
