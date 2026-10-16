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
	if (state != "running" && state != "pause_requested") || worker != lease.WorkerID || fence != lease.Fence || expires <= time.Now().UnixNano() {
		return ErrConflict
	}
	if value.Artifact.GroupID != group {
		return fmt.Errorf("checkpoint artifact group mismatch")
	}
	if err = requireSealedArtifact(ctx, tx, value.Artifact); err != nil {
		return err
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

// ReadWorkflowCheckpoint verifies a node-boundary snapshot against its immutable
// terminal event and root workflow. It rejects arbitrary input/result artifacts.
// Callers must separately authorize current source access before exposing it.
func (s *Store) ReadWorkflowCheckpoint(ctx context.Context, group, run, id string) (RunCheckpoint, WorkflowCheckpoint, error) {
	var checkpoint RunCheckpoint
	var snapshot WorkflowCheckpoint
	if !manifest.ValidID(group) || !manifest.ValidID(run) || !manifest.ValidID(id) {
		return checkpoint, snapshot, fmt.Errorf("invalid checkpoint scope")
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT c.body FROM run_checkpoints c JOIN runs r ON r.id=c.run_id WHERE r.group_id=? AND c.run_id=? AND c.id=?", group, run, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint, snapshot, ErrNotFound
	}
	if err != nil {
		return checkpoint, snapshot, err
	}
	checkpoint, err = evolution.Decode[RunCheckpoint](bytes.NewReader(raw))
	if err != nil || checkpoint.ID != id || checkpoint.RunID != run || checkpoint.Artifact.GroupID != group || checkpoint.Artifact.SchemaDigest != WorkflowCheckpointSchema {
		return RunCheckpoint{}, snapshot, ErrConflict
	}
	raw, err = s.Artifact(ctx, checkpoint.Artifact)
	if err != nil {
		return checkpoint, snapshot, err
	}
	snapshot, err = evolution.Decode[WorkflowCheckpoint](bytes.NewReader(raw))
	if err != nil || snapshot.RunID != run || snapshot.Workflow.Validate() != nil || snapshot.Through < 1 {
		return checkpoint, WorkflowCheckpoint{}, ErrConflict
	}
	source, err := s.Run(ctx, group, run)
	if err != nil {
		return checkpoint, snapshot, err
	}
	if source.Pins.Workflow != snapshot.Workflow || source.Pins.SourcePolicyRevision != checkpoint.Artifact.PolicyRevision {
		return checkpoint, snapshot, ErrConflict
	}
	if err = s.db.QueryRowContext(ctx, "SELECT request FROM run_events WHERE run_id=? AND sequence=?", run, snapshot.Through).Scan(&raw); err != nil {
		return checkpoint, snapshot, err
	}
	event, err := evolution.Decode[evolution.Event](bytes.NewReader(raw))
	if err != nil || event.RunID != run || checkpoint.ID != "node-"+event.AttemptID || event.Fence != checkpoint.Fence || (event.Kind != "attempt-succeeded" && event.Kind != "attempt-failed" && event.Kind != "attempt-skipped") {
		return checkpoint, snapshot, ErrConflict
	}
	for _, artifact := range event.Artifacts {
		if artifact == checkpoint.Artifact {
			return checkpoint, snapshot, nil
		}
	}
	return checkpoint, snapshot, ErrConflict
}
