package memorystore

import (
	"context"
	"fmt"
	"time"
)

// UpsertRawSource records (or refreshes) the pointer to a session's archived
// native trace. Keyed by (session_id, path); bytes/mtime enable drift detection.
func (s *Store) UpsertRawSource(ctx context.Context, sessionID, path, kind, nativeSessionID string, bytes int64, mtime time.Time) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO raw_sources (session_id, path, kind, native_session_id, bytes, mtime, captured_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, path) DO UPDATE SET
  kind = excluded.kind,
  native_session_id = excluded.native_session_id,
  bytes = excluded.bytes,
  mtime = excluded.mtime,
  captured_at = excluded.captured_at`,
		sessionID, path, kind, nativeSessionID, bytes, formatTime(mtime), formatTime(now))
	if err != nil {
		return fmt.Errorf("upsert raw source: %w", err)
	}
	return nil
}

// ReplaceTrajectoryEvents wholesale-replaces a session's normalized event stream
// (the rebuildable half) and upserts its trajectory_snapshots index row. It never
// touches the distilled memory/skill tables. Used by the native-capture path;
// events already carry NativeID/ParentID/CallID from the pi parser.
//
// The replace is chunked across several short transactions (see
// rebuildTrajectoryEvents in trajectory.go) so it never holds this store's single
// write connection for a whole multi-thousand-event capture.
//
// snap carries DENORMALIZED CONTROL VALUES (status, user_id, started_at,
// ended_at) that the caller read from the control plane earlier — for the native
// path, from the ListPiSessionsForCapture enumeration at the top of the sweep,
// which can be many minutes before this write. They are stored as a
// point-in-time copy with no referential integrity; see the note on
// TrajectorySnapshot and the torn-read discussion on Ingestor.SnapshotSession.
// Nothing reads them back as live session state.
func (s *Store) ReplaceTrajectoryEvents(ctx context.Context, snap TrajectorySnapshot, events []TrajectoryEvent) error {
	// The idx a caller passed in is not trusted here: this path has always
	// renumbered events 0..n-1 by slice position.
	renumbered := make([]TrajectoryEvent, len(events))
	for i, ev := range events {
		ev.Idx = i
		renumbered[i] = ev
	}
	if err := s.rebuildTrajectoryEvents(ctx, snap, renumbered); err != nil {
		return fmt.Errorf("replace trajectory events: %w", err)
	}
	return nil
}
