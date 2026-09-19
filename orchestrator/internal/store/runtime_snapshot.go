package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

// ErrTurnNotCancelable reports that the turn was not withdrawable: it is no
// longer queued (the worker claimed it, or it already ended), or it does not
// belong to this session. An expected outcome of a click racing the worker, not
// a fault — callers map it to 409.
var ErrTurnNotCancelable = errors.New("turn is not cancelable")

// The session runtime projection: the single durable answer to "what is this
// session doing, and what is waiting behind it".
//
// Before this existed the UI derived that answer from three disagreeing sources
// — the coarse sessions.status, a client-side event FSM, and a guess at the
// phase taken from the last durable event of the turn. The last one is the
// reason a refresh mid-tool showed "responding": for a running turn the newest
// event is almost always an agent.delta, not the tool frame. Phase is now
// written onto the turn row as it happens (see SetTurnPhase), so reading it back
// is one indexed lookup and needs no reconstruction.

// TurnPhase is the coarse activity of a turn that is currently executing. It is
// deliberately small: anything finer (per-token streaming) is a live-only
// concern that the client refines on top of the snapshot.
type TurnPhase string

const (
	TurnPhaseNone       TurnPhase = ""
	TurnPhaseThinking   TurnPhase = "thinking"
	TurnPhaseTool       TurnPhase = "tool"
	TurnPhaseStreaming  TurnPhase = "streaming"
	TurnPhaseCompacting TurnPhase = "compacting"
	TurnPhaseRetrying   TurnPhase = "retrying"
)

// ValidTurnPhase gates every write to turns.phase. Databases upgraded by
// ensureTurnColumns carry no CHECK constraint (SQLite cannot add one), so this
// is the only enforcement point — keep it as the single writer's gate.
func ValidTurnPhase(phase TurnPhase) bool {
	switch phase {
	case TurnPhaseNone, TurnPhaseThinking, TurnPhaseTool, TurnPhaseStreaming, TurnPhaseCompacting, TurnPhaseRetrying:
		return true
	default:
		return false
	}
}

// TurnSnapshot is one turn as the UI needs to see it.
type TurnSnapshot struct {
	ID       int64  `json:"id"`
	Sequence int64  `json:"sequence"`
	Content  string `json:"content"`
	Status   string `json:"status"`
	// Phase is meaningful only for the active turn; a queued turn has none.
	Phase      TurnPhase  `json:"phase,omitempty"`
	PhaseAt    *time.Time `json:"phase_at,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolDetail string     `json:"tool_detail,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// Parked means the worker will not claim this turn until the user releases it
	// — the state Stop leaves the rest of the queue in. The queue row renders
	// differently for it: a parked turn is waiting on the user, not on the agent,
	// so presenting it as "up next" would be a lie the user cannot act on.
	Parked bool `json:"parked,omitempty"`
}

// SessionRuntimeSnapshot is the whole contract. The same value is served by
// GET /api/sessions/{id}/runtime and pushed as the `session.runtime` event, so
// a client has exactly one shape to apply however it arrives.
// SandboxState is the user-facing answer to "is there a sandbox behind this
// session right now", derived from the active generation and its resource row.
// sessions.status cannot answer it: running_idle covers both a warm sandbox
// waiting for the next turn and a session whose sandbox died or was released,
// which differ by a cold start (tens of seconds) on the next message.
type SandboxState string

const (
	// SandboxLive: a generation is active or idle with live resources; the next
	// message is picked up immediately.
	SandboxLive SandboxState = "live"
	// SandboxStarting: a generation is being allocated/started/probed/restored.
	SandboxStarting SandboxState = "starting"
	// SandboxReleased: no usable generation (none yet, failed, destroyed, or
	// released); the next message cold-starts one.
	SandboxReleased SandboxState = "released"
)

// deriveSandboxState maps the active generation's status and resource state
// onto SandboxState. Empty inputs mean "no active generation".
func deriveSandboxState(generationStatus, resourceState string) SandboxState {
	switch generationStatus {
	case "active", "idle":
		switch resourceState {
		case "live", "ready", "allocating", "recreating":
			return SandboxLive
		}
		return SandboxReleased
	case "allocating", "starting", "probing", "restoring":
		return SandboxStarting
	default:
		return SandboxReleased
	}
}

type SessionRuntimeSnapshot struct {
	SessionID     string `json:"session_id"`
	SessionStatus string `json:"session_status"`
	// Sandbox reports whether a sandbox currently backs the session. Carried in
	// the snapshot because this is the contract every status change pushes or
	// re-reads; the session list carries the same field for the initial render.
	Sandbox SandboxState `json:"sandbox"`
	// Revision is monotonic per session. A client MUST discard a snapshot whose
	// revision is not greater than the one it already holds: an in-flight GET can
	// otherwise land after a newer push and resurrect a drained queue entry,
	// which then never clears because nothing contradicts it again.
	Revision int64 `json:"revision"`
	// ActiveTurn is the turn the agent is executing (leased or running), or nil
	// when the session is idle. This — not sessions.status — is what "the agent
	// is busy" means: a session stays running_active while a queued turn waits
	// for a generation, and reports running_active briefly before any turn is
	// claimed.
	ActiveTurn *TurnSnapshot `json:"active_turn"`
	// QueuedTurns are the user's own pending turns, in execution order.
	QueuedTurns []TurnSnapshot `json:"queued_turns"`
}

const turnSnapshotColumns = `id, sequence, content, status, phase, phase_at, phase_tool, phase_detail, started_at, created_at, auto_dispatch`

// GetSessionRuntimeSnapshot reads the durable projection for one session.
func (s *Store) GetSessionRuntimeSnapshot(ctx context.Context, sessionID string) (SessionRuntimeSnapshot, error) {
	// Both queries must observe the same database snapshot. Otherwise a concurrent
	// commit can pair an old revision with new turns (or the reverse).
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := getSessionRuntimeSnapshot(ctx, tx, sessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	return snapshot, tx.Commit()
}

// queryer is the read surface shared by *sql.DB and *sql.Tx, so the snapshot can
// be taken inside the transaction that just changed it (giving the caller a
// value that already carries the bumped revision) or standalone.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func getSessionRuntimeSnapshot(ctx context.Context, db queryer, sessionID string) (SessionRuntimeSnapshot, error) {
	var out SessionRuntimeSnapshot
	var generationStatus, resourceState string
	if err := db.QueryRowContext(ctx, `
SELECT s.status, s.turn_revision, COALESCE(g.status, ''), COALESCE(CASE WHEN b.state='accepted' AND b.environment_fence=e.fence AND b.epoch=e.epoch AND NOT EXISTS(SELECT 1 FROM environment_reconcile_requests q WHERE q.environment_id=b.environment_id) THEN 'live' WHEN b.state='starting' THEN 'allocating' WHEN b.generation_id IS NOT NULL THEN 'destroyed' END,r.resource_state,'')
FROM sessions s
LEFT JOIN runtime_generations g ON g.generation_id = s.active_generation_id
LEFT JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
LEFT JOIN execution_bindings b ON b.generation_id=g.generation_id
LEFT JOIN sandbox_environments e ON e.environment_id=b.environment_id
WHERE s.id = ?`, sessionID).Scan(&out.SessionStatus, &out.Revision, &generationStatus, &resourceState); err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	out.SessionID = sessionID
	out.Sandbox = deriveSandboxState(generationStatus, resourceState)
	out.QueuedTurns = []TurnSnapshot{}

	// Deliberately NOT pendingTurnPredicate: a parked turn is still the user's
	// pending prompt and has to stay visible, or Stop would look like it discarded
	// the queue after all. Parked-ness is carried per row instead, so the client can
	// render it as waiting-on-you rather than waiting-on-the-agent.
	rows, err := db.QueryContext(ctx, `
SELECT `+turnSnapshotColumns+`
FROM turns
WHERE session_id = ?
  AND status IN ('queued','leased','running')
ORDER BY sequence`, sessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		turn, err := scanTurnSnapshot(rows)
		if err != nil {
			return SessionRuntimeSnapshot{}, err
		}
		if turn.Status == "queued" {
			// A queued turn has not run, so it carries no activity.
			turn.Phase, turn.PhaseAt, turn.ToolName, turn.ToolDetail = TurnPhaseNone, nil, "", ""
			out.QueuedTurns = append(out.QueuedTurns, turn)
			continue
		}
		if out.ActiveTurn == nil {
			active := turn
			// A leased turn has been handed to the bridge but has not acked start
			// yet. It is genuinely active; report it as thinking rather than
			// leaving the strip blank during handoff.
			if active.Phase == TurnPhaseNone {
				active.Phase = TurnPhaseThinking
			}
			out.ActiveTurn = &active
			continue
		}
		// The state machine permits at most one active turn per session
		// (ClaimNextTurn's NOT EXISTS guard). Should a repair path ever leave a
		// second one, surface it as queued rather than dropping the user's work.
		out.QueuedTurns = append(out.QueuedTurns, turn)
	}
	if err := rows.Err(); err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	return out, nil
}

func scanTurnSnapshot(row rowScanner) (TurnSnapshot, error) {
	var (
		turn      TurnSnapshot
		phase     sql.NullString
		phaseAt   sql.NullString
		tool      sql.NullString
		detail    sql.NullString
		startedAt sql.NullString
		createdAt sql.NullString
		dispatch  int
	)
	if err := row.Scan(&turn.ID, &turn.Sequence, &turn.Content, &turn.Status,
		&phase, &phaseAt, &tool, &detail, &startedAt, &createdAt, &dispatch); err != nil {
		return TurnSnapshot{}, err
	}
	turn.Parked = dispatch == 0
	if phase.Valid {
		turn.Phase = TurnPhase(phase.String)
	}
	turn.PhaseAt = optionalTimePtr(phaseAt)
	turn.StartedAt = optionalTimePtr(startedAt)
	if tool.Valid {
		turn.ToolName = tool.String
	}
	if detail.Valid {
		turn.ToolDetail = detail.String
	}
	if createdAt.Valid {
		turn.CreatedAt = parseTime(createdAt.String)
	}
	return turn, nil
}

// SetTurnPhaseParams describes one coarse activity transition.
type SetTurnPhaseParams struct {
	SessionID  string
	TurnID     int64
	Phase      TurnPhase
	ToolName   string
	ToolDetail string
	Now        time.Time
}

// SetTurnPhase records what an executing turn is doing now, returning the
// resulting snapshot so the caller can publish it without a second read.
//
// Scoped to leased/running turns on purpose: a phase frame that arrives after
// the turn completed (or for a turn the user just canceled) must not reanimate
// it. In that case the write is a no-op and the returned snapshot is simply the
// current truth, which is what the client should render anyway.
//
// Callers are expected to dedupe in memory and to skip per-token frames — this
// is a coarse projection, not a stream.
func (s *Store) SetTurnPhase(ctx context.Context, p SetTurnPhaseParams) (SessionRuntimeSnapshot, error) {
	if strings.TrimSpace(p.SessionID) == "" {
		return SessionRuntimeSnapshot{}, fmt.Errorf("session id is required")
	}
	if !ValidTurnPhase(p.Phase) || p.Phase == TurnPhaseNone {
		return SessionRuntimeSnapshot{}, fmt.Errorf("invalid turn phase %q", p.Phase)
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
UPDATE turns
SET phase = ?,
    phase_at = ?,
    phase_tool = ?,
    phase_detail = ?
WHERE id = ?
  AND session_id = ?
  AND status IN ('leased','running')
  AND (phase <> ? OR COALESCE(phase_tool,'') <> ? OR COALESCE(phase_detail,'') <> ?)`,
		string(p.Phase), formatTime(p.Now), nullableString(p.ToolName), nullableString(p.ToolDetail),
		p.TurnID, p.SessionID,
		string(p.Phase), p.ToolName, p.ToolDetail); err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	// A redundant frame matches nothing (the WHERE clause compares the incoming
	// phase/tool against the stored one), so the revision trigger does not fire
	// and every client keeps its cached snapshot.
	snapshot, err := getSessionRuntimeSnapshot(ctx, tx, p.SessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	return snapshot, tx.Commit()
}

// CancelQueuedTurnParams identifies a queued turn to withdraw.
type CancelQueuedTurnParams struct {
	SessionID string
	TurnID    int64
	Now       time.Time
}

// CancelQueuedTurn withdraws a turn the user queued but no longer wants.
//
// Only a 'queued' turn can be withdrawn: once leased the agent may already be
// acting on it, and cancelling that is Stop's job (InterruptSession), which also
// has to tear down the generation. Returns ErrTurnNotCancelable when the turn
// moved on, so a click racing the worker's claim is reported honestly instead of
// silently doing nothing.
//
// The message row is deleted along with the turn. The turn never executed, so
// leaving the text in the transcript would show the user asking something that
// was never asked — and on the next load it would read as an unanswered turn.
func (s *Store) CancelQueuedTurn(ctx context.Context, p CancelQueuedTurnParams) (SessionRuntimeSnapshot, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'canceled',
    completed_at = ?,
    error_class = 'user_canceled',
    error = 'canceled by user before dispatch',
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE id = ?
  AND session_id = ?
  AND status = 'queued'`, formatTime(p.Now), p.TurnID, p.SessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	if affected != 1 {
		return SessionRuntimeSnapshot{}, ErrTurnNotCancelable
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM messages WHERE session_id = ? AND turn_id = ?`, p.SessionID, p.TurnID); err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	// Withdrawing the last pending turn returns the session to idle, so the
	// composer stops reporting the agent as busy.
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?
WHERE id = ?
  AND status = ?
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE session_id = ?
      AND `+pendingTurnPredicate+`
  )`,
		string(sessionstate.RunningIdle), formatTime(p.Now), p.SessionID,
		string(sessionstate.RunningActive), p.SessionID); err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	snapshot, err := getSessionRuntimeSnapshot(ctx, tx, p.SessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	return snapshot, tx.Commit()
}

// SessionSandboxStates returns the SandboxState of every session in one query,
// for the session list. Sessions without an active generation are reported as
// released.
func (s *Store) SessionSandboxStates(ctx context.Context) (map[string]SandboxState, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT s.id, COALESCE(g.status, ''), COALESCE(CASE WHEN b.state='accepted' AND b.environment_fence=e.fence AND b.epoch=e.epoch AND NOT EXISTS(SELECT 1 FROM environment_reconcile_requests q WHERE q.environment_id=b.environment_id) THEN 'live' WHEN b.state='starting' THEN 'allocating' WHEN b.generation_id IS NOT NULL THEN 'destroyed' END,r.resource_state,'')
FROM sessions s
LEFT JOIN runtime_generations g ON g.generation_id = s.active_generation_id
LEFT JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
LEFT JOIN execution_bindings b ON b.generation_id=g.generation_id
LEFT JOIN sandbox_environments e ON e.environment_id=b.environment_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]SandboxState)
	for rows.Next() {
		var id, generationStatus, resourceState string
		if err := rows.Scan(&id, &generationStatus, &resourceState); err != nil {
			return nil, err
		}
		states[id] = deriveSandboxState(generationStatus, resourceState)
	}
	return states, rows.Err()
}
