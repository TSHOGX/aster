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

// AppendRunEvent accepts observations only from the current live worker. Sequence
// numbers are allocated by the store; producer-supplied sequence is rejected.
func (s *Store) AppendRunEvent(ctx context.Context, lease RunLease, event evolution.Event) (evolution.Event, error) {
	if !manifest.ValidID(event.ID) || event.RunID != lease.RunID || event.ProducerID != lease.WorkerID || event.Fence != lease.Fence || event.Sequence != 0 || event.Timestamp.IsZero() || !manifest.ValidID(event.Kind) {
		return event, fmt.Errorf("invalid run event identity")
	}
	if event.AttemptID != "" && !manifest.ValidID(event.AttemptID) {
		return event, fmt.Errorf("invalid attempt identity")
	}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return event, fmt.Errorf("invalid event size")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	var group, state, worker string
	var fence, expires int64
	if err = tx.QueryRowContext(ctx, "SELECT group_id,state,worker_id,fence,lease_until FROM runs WHERE id=?", lease.RunID).Scan(&group, &state, &worker, &fence, &expires); err != nil {
		return event, err
	}
	if (state != "running" && state != "pause_requested") || worker != lease.WorkerID || fence != lease.Fence || expires <= time.Now().UnixNano() {
		return event, ErrConflict
	}
	artifacts := map[string]bool{}
	for _, artifact := range event.Artifacts {
		if err = artifact.Validate(); err != nil {
			return event, err
		}
		if artifact.GroupID != group || artifacts[artifact.ID] {
			return event, fmt.Errorf("event artifact group mismatch or duplicate identity")
		}
		artifacts[artifact.ID] = true
		if err = requireSealedArtifact(ctx, tx, artifact); err != nil {
			return event, err
		}
	}
	var stored []byte
	var sequence int64
	err = tx.QueryRowContext(ctx, "SELECT request,sequence FROM run_events WHERE run_id=? AND event_id=?", event.RunID, event.ID).Scan(&stored, &sequence)
	if err == nil {
		if !bytes.Equal(stored, raw) {
			return event, ErrConflict
		}
		event.Sequence = sequence
		return event, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return event, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0)+1 FROM run_events WHERE run_id=?", event.RunID).Scan(&sequence); err != nil {
		return event, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_events(run_id,event_id,sequence,request) VALUES(?,?,?,?)", event.RunID, event.ID, sequence, raw); err != nil {
		return event, err
	}
	event.Sequence = sequence
	return event, tx.Commit()
}

type RunEventPage struct {
	Events    []evolution.Event `json:"events"`
	NextAfter int64             `json:"next_after"`
	HasMore   bool              `json:"has_more"`
}

// RunEvents is internal storage access; API callers must check trace/artifact
// permissions before returning event contents to users.
func (s *Store) RunEvents(ctx context.Context, group, run string, after int64, limit int) (RunEventPage, error) {
	result := RunEventPage{Events: []evolution.Event{}, NextAfter: after}
	if !manifest.ValidID(group) || !manifest.ValidID(run) || after < 0 || limit < 1 || limit > 100 {
		return result, fmt.Errorf("invalid event page")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM runs WHERE group_id=? AND id=?", group, run).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT sequence,request FROM run_events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?", run, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var raw []byte
		if err = rows.Scan(&sequence, &raw); err != nil {
			return result, err
		}
		if len(result.Events) == limit {
			result.HasMore = true
			break
		}
		event, err := evolution.Decode[evolution.Event](bytes.NewReader(raw))
		if err != nil || event.RunID != run {
			return result, fmt.Errorf("invalid stored run event")
		}
		event.Sequence = sequence
		result.Events = append(result.Events, event)
		result.NextAfter = sequence
	}
	return result, rows.Err()
}
