package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PiSessionForCapture is one row of the enumeration a full-fidelity trajectory
// capture walks: everything needed to locate a session's native pi JSONL on the
// host without any further per-session query.
type PiSessionForCapture struct {
	SessionID string
	UserID    string
	Status    string
	// AgentHomeHostPath is the persistent host dir bind-mounted at /agent-home
	// (session_driver_homes.host_path, e.g. <AgentHomesRoot>/<session>/pi). The
	// native pi sessions live under <AgentHomeHostPath>/.pi/agent/sessions/.
	AgentHomeHostPath string
	// SelectedSessionRelpath is the canonical native session file pi reported at
	// the last completed turn (relative to .pi/agent/sessions/). Empty when the
	// session never advanced to pi_session state; callers may glob instead.
	SelectedSessionRelpath string
	// NativeSessionID is pi's own session uuid (selected_session_id), when known.
	NativeSessionID string
	EndedAt         *time.Time
}

// ListPiSessionsForCapture enumerates pi sessions with a provisioned driver home
// for trajectory capture, across ALL users. When since is non-zero only sessions
// that ended at/after it (or never recorded an ended_at) are returned, so a
// nightly/replay run can scope to a window. Sessions without a driver home are
// skipped (nothing to capture yet).
func (s *Store) ListPiSessionsForCapture(ctx context.Context, since time.Time) ([]PiSessionForCapture, error) {
	query := `
SELECT s.id, s.user_id, s.status, h.host_path, st.state_payload, s.ended_at
FROM sessions s
JOIN session_driver_homes h ON h.session_id = s.id AND h.driver = ?
LEFT JOIN session_driver_states st ON st.session_id = s.id AND st.driver_id = ?`
	args := []any{driverPi, driverPi}
	if !since.IsZero() {
		query += `
WHERE (s.ended_at IS NULL OR s.ended_at >= ?)`
		args = append(args, formatTime(since))
	}
	query += `
ORDER BY s.ended_at`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list pi sessions for capture: %w", err)
	}
	defer rows.Close()

	var out []PiSessionForCapture
	for rows.Next() {
		var (
			rec     PiSessionForCapture
			payload sql.NullString
			endedAt sql.NullString
		)
		if err := rows.Scan(&rec.SessionID, &rec.UserID, &rec.Status, &rec.AgentHomeHostPath, &payload, &endedAt); err != nil {
			return nil, fmt.Errorf("scan pi session for capture: %w", err)
		}
		if payload.Valid && strings.TrimSpace(payload.String) != "" {
			rec.SelectedSessionRelpath, rec.NativeSessionID = parsePiSelectedSession(payload.String)
		}
		if endedAt.Valid && endedAt.String != "" {
			t := parseTime(endedAt.String)
			rec.EndedAt = &t
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

const driverPi = "pi"

// parsePiSelectedSession pulls selected_session_relpath + selected_session_id out
// of a pi driver-state payload. Best-effort: returns empties on any shape it
// doesn't recognize (e.g. the pi_uninitialized state).
func parsePiSelectedSession(payload string) (relpath, nativeID string) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return "", ""
	}
	relpath, _ = obj["selected_session_relpath"].(string)
	nativeID, _ = obj["selected_session_id"].(string)
	return strings.TrimSpace(relpath), strings.TrimSpace(nativeID)
}

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
func (s *Store) ReplaceTrajectoryEvents(ctx context.Context, snap TrajectorySnapshot, events []TrajectoryEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace trajectory events: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM trajectory_events WHERE session_id = ?`, snap.SessionID); err != nil {
		return fmt.Errorf("replace trajectory events: clear: %w", err)
	}
	for i, ev := range events {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO trajectory_events (
  session_id, idx, kind, subtype, name, call_id, is_error, native_id, parent_id, message_id, tokens, text, ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snap.SessionID, i, ev.Kind,
			nullableString(ev.Subtype), nullableString(ev.Name), nullableString(ev.CallID),
			boolInt(ev.IsError), ev.NativeID, ev.ParentID, ev.MessageID, ev.Tokens, ev.Text, formatOptionalTime(ev.TS),
		); err != nil {
			return fmt.Errorf("replace trajectory events: insert %d: %w", i, err)
		}
	}

	if err := upsertTrajectorySnapshotTx(ctx, tx, snap); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace trajectory events: commit: %w", err)
	}
	return nil
}
