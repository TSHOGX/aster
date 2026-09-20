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

// Turn resume: continuing a turn whose model call died on a transient upstream
// fault (rate limit, lost proxy context) instead of leaving the reply truncated.
//
// The platform's standing invariant is that a turn which already began executing
// is never re-dispatched — requeueExpiredLeasedTurnsTx only requeues turns with
// ack_started_at IS NULL — because replaying one could repeat tool calls that
// already had side effects. Resume does not break that invariant: the failed turn
// stays terminal, and a *new* turn is appended that asks the agent to carry on.
// The agent's own pi session still holds the prior context, so continuing is
// additive rather than a replay.
//
// A resume turn records its bounded continuation depth in retry_policy.

// resumeRetryPolicyPrefix marks a turn as an automatic continuation. Turns
// without this prefix are user-authored and always resumable from attempt 1.
const resumeRetryPolicyPrefix = "resume:"

// ErrTurnNotResumable reports that the turn was examined and deliberately not
// continued. It is an expected outcome, not a failure: the caller logs at debug
// and moves on.
var ErrTurnNotResumable = errors.New("turn is not resumable")

type ResumeFailedTurnParams struct {
	SessionID string
	// TurnID is the failed turn to continue.
	TurnID int64
	// Content is the continuation prompt handed to the agent.
	Content string
	// MaxAttempts caps automatic continuations per user turn.
	MaxAttempts int
	Now         time.Time
}

type ResumeFailedTurnResult struct {
	// TurnID of the newly queued continuation turn.
	TurnID int64
	// Attempt is this continuation's 1-based number.
	Attempt int
}

// ResumeFailedTurn appends a continuation turn for a failed turn, in one
// transaction, and flips the session back to running so the bridge dispatches
// it.
//
// Returns ErrTurnNotResumable, having changed nothing, when any guard fails:
//   - the turn is not terminal-failed, or does not belong to the session
//   - the session is gone, ended, or has no live generation
//   - work is already in flight (a queued/leased/running turn), so continuing
//     would double-drive the session
//   - the continuation budget for this user turn is exhausted
//
// Deliberately writes no `messages` row. The user did not type this, and showing
// a synthetic prompt in the transcript would misrepresent who asked for it.
func (s *Store) ResumeFailedTurn(ctx context.Context, p ResumeFailedTurnParams) (ResumeFailedTurnResult, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.Content) == "" {
		return ResumeFailedTurnResult{}, fmt.Errorf("resume content is required")
	}
	if p.MaxAttempts <= 0 {
		return ResumeFailedTurnResult{}, fmt.Errorf("resume max attempts must be > 0")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// The failed turn must exist, belong to this session, and be terminally
	// failed. Anything else (still running, already completed, canceled by the
	// user) must not be continued behind the user's back.
	var (
		sequence    int64
		retryPolicy sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
SELECT sequence, retry_policy
FROM turns
WHERE id = ?
  AND session_id = ?
  AND status = 'failed'`, p.TurnID, p.SessionID).Scan(&sequence, &retryPolicy)
	if errors.Is(err, sql.ErrNoRows) {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}

	var superseded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND sequence>?`, p.SessionID, sequence).Scan(&superseded); err != nil {
		return ResumeFailedTurnResult{}, err
	}
	if superseded != 0 {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}
	var generation, owner string
	if err := tx.QueryRowContext(ctx, `SELECT b.generation_id,b.owner FROM execution_bindings b JOIN sessions s ON s.active_generation_id=b.generation_id WHERE s.id=? AND b.state='accepted'`, p.SessionID).Scan(&generation, &owner); err != nil {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}
	if err := acceptedExecutionTx(ctx, tx, p.SessionID, generation, owner, p.Now); err != nil {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}

	// Continuations chain: resuming a resume increments the same budget, so a
	// persistent fault cannot walk the session forward indefinitely one hop at a
	// time.
	attempt, err := resumeAttemptNumber(retryPolicy)
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}
	if attempt > p.MaxAttempts {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}

	// Nothing may be in flight. A queued turn means the user already sent their
	// own follow-up, which takes precedence over an automatic continuation.
	var inflight int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND `+pendingTurnPredicate+``, p.SessionID).Scan(&inflight); err != nil {
		return ResumeFailedTurnResult{}, err
	}
	if inflight != 0 {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}

	// Only continue into a session that still has a live generation to run it.
	// Without this the continuation would sit queued against a dead sandbox.
	var liveGeneration int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sessions s
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
WHERE s.id = ?
  AND s.status IN (?, ?, ?)
  AND g.status IN ('active', 'idle')`,
		p.SessionID,
		string(sessionstate.Created), string(sessionstate.RunningIdle), string(sessionstate.RunningActive),
	).Scan(&liveGeneration); err != nil {
		return ResumeFailedTurnResult{}, err
	}
	if liveGeneration == 0 {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}

	next, err := nextTurnSequence(ctx, tx, p.SessionID)
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}
	// Guard against a racing insert between the in-flight check and this one:
	// the new turn must be strictly after the turn it continues.
	if next <= sequence {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO turns (session_id, sequence, role, content, status, attempt, retry_policy, created_at)
VALUES (?, ?, 'user', ?, 'queued', 0, ?, ?)`,
		p.SessionID, next, p.Content, resumeRetryPolicy(attempt), formatTime(p.Now))
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}
	newTurnID, err := res.LastInsertId()
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}

	// Flip the session back to running so the bridge picks the turn up. Scoped to
	// the idle-ish states: a session that changed underneath us fails the CAS and
	// the whole resume rolls back.
	upd, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    last_activity_at = ?
WHERE id = ?
  AND status IN (?, ?, ?)`,
		string(sessionstate.RunningActive), formatTime(p.Now), formatTime(p.Now), p.SessionID,
		string(sessionstate.Created), string(sessionstate.RunningIdle), string(sessionstate.RunningActive))
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}
	affected, err := upd.RowsAffected()
	if err != nil {
		return ResumeFailedTurnResult{}, err
	}
	if affected != 1 {
		return ResumeFailedTurnResult{}, ErrTurnNotResumable
	}

	if err := tx.Commit(); err != nil {
		return ResumeFailedTurnResult{}, err
	}
	return ResumeFailedTurnResult{TurnID: newTurnID, Attempt: attempt}, nil
}

// resumeRetryPolicy renders the marker stored on a continuation turn.
func resumeRetryPolicy(attempt int) string {
	return fmt.Sprintf("%s%d", resumeRetryPolicyPrefix, attempt)
}

// resumeAttemptNumber returns the continuation number to use for a turn carrying
// the given retry_policy. A user-authored turn (no marker) yields 1; resuming a
// continuation yields its number plus one.
func resumeAttemptNumber(retryPolicy sql.NullString) (int, error) {
	if !retryPolicy.Valid {
		return 1, nil
	}
	raw := strings.TrimSpace(retryPolicy.String)
	if raw == "" {
		return 1, nil
	}
	if !strings.HasPrefix(raw, resumeRetryPolicyPrefix) {
		// Some other policy we do not own; treat as a first continuation rather
		// than guessing at its meaning.
		return 1, nil
	}
	var previous int
	if _, err := fmt.Sscanf(raw, resumeRetryPolicyPrefix+"%d", &previous); err != nil {
		return 0, fmt.Errorf("malformed resume retry policy %q: %w", raw, err)
	}
	if previous < 1 {
		return 0, fmt.Errorf("malformed resume retry policy %q", raw)
	}
	return previous + 1, nil
}

type TurnContinuation struct {
	SessionID string
	TurnID    int64
}

func (s *Store) PendingTurnContinuations(ctx context.Context, after string, cutoff time.Time, limit int) ([]TurnContinuation, error) {
	if limit <= 0 || limit > 128 {
		limit = 32
	}
	rows, err := s.reader().QueryContext(ctx, `SELECT t.session_id,t.id FROM turns t JOIN sessions s ON s.id=t.session_id JOIN execution_bindings b ON b.generation_id=s.active_generation_id
 WHERE t.session_id>? AND t.status='failed' AND t.completed_at<=? AND t.error_class IN ('upstream_rate_limit','proxy_context_lost','upstream_timeout','upstream_5xx')
 AND b.state='accepted' AND b.detached=0 AND s.status IN ('running_idle','running_active')
 AND NOT EXISTS(SELECT 1 FROM turns newer WHERE newer.session_id=t.session_id AND newer.sequence>t.sequence)
 AND NOT EXISTS(SELECT 1 FROM session_retirements r WHERE r.session_id=t.session_id)
 ORDER BY t.session_id LIMIT ?`, after, formatTime(cutoff), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []TurnContinuation
	for rows.Next() {
		var candidate TurnContinuation
		if err := rows.Scan(&candidate.SessionID, &candidate.TurnID); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}
