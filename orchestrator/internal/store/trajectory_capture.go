package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PiSessionForCaptureRow is one row of the enumeration a full-fidelity trajectory
// capture walks: everything needed to locate a session's native pi JSONL on the
// host without any further per-session query.
//
// This is the CONTROL-side half of the ingest boundary. The query below is a pure
// control-plane 3-way join (sessions / session_driver_homes /
// session_driver_states), so it stays here with the tables it reads. The memory
// half declares the shape it wants as memorystore.PiSessionForCapture, and the
// adapter in internal/controlsource maps this row onto it. Two structs rather
// than one shared type is what keeps the dependency unidirectional: this package
// must not know memorystore exists.
type PiSessionForCaptureRow struct {
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
func (s *Store) ListPiSessionsForCapture(ctx context.Context, since time.Time) ([]PiSessionForCaptureRow, error) {
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

	var out []PiSessionForCaptureRow
	for rows.Next() {
		var (
			rec     PiSessionForCaptureRow
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
