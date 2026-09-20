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

// Parking: what Stop does to the turns behind the one it stopped.
//
// Stop used to cancel the whole queue and say so. That threw away prompts the
// user was still waiting on, so the queue now survives — but it must not simply
// keep running either, because "Stop" plainly means stop. Parking is that third
// state: the turn stays queued and visible, and the worker will not claim it
// until the user releases it.
//
// It is a column rather than a turn status because turns.status carries a CHECK
// constraint, and widening one in SQLite means rebuilding the table (see
// ensureScheduleRunStatusCheck). A boolean added by ensureTurnColumns is an O(1)
// metadata-only ALTER, and orthogonal besides: parked-ness is about who may
// dispatch the turn, not about how far it has got.

// pendingTurnPredicate is the "this session owes work" test, as SQL, for a query
// that refers to the turns table unqualified.
//
// Every site that used to read `status IN ('queued','leased','running')` reads
// this instead. A parked turn is deliberately NOT pending: nothing will dispatch
// it until the user acts, so counting it would keep the generation active, hold a
// sandbox warm indefinitely, and block pausing forever — for a prompt that
// may never be released. Releasing a parked turn costs a cold start, exactly as
// retyping it would.
//
// Only a queued turn is ever parked (parkQueuedTurnsTx), so this never hides a
// turn that is actually executing.
const pendingTurnPredicate = `status IN ('queued', 'leased', 'running') AND auto_dispatch = 1`

// pendingTurnPredicateFor is pendingTurnPredicate for a query that qualifies the
// turns table (an alias, or the table name in a correlated subquery).
func pendingTurnPredicateFor(alias string) string {
	return fmt.Sprintf(`%[1]s.status IN ('queued', 'leased', 'running') AND %[1]s.auto_dispatch = 1`, alias)
}

// ErrTurnNotReleasable reports that a turn could not be released: it is not
// parked, it already ran, or it does not belong to this session. Like
// ErrTurnNotCancelable this is an expected outcome of a click racing a state
// change, and callers map it to 409.
var ErrTurnNotReleasable = errors.New("turn is not releasable")

// ErrTurnNotSteerable reports that a turn could not be steered into the running
// turn: the target turn is no longer executing, or the queued row moved on.
var ErrTurnNotSteerable = errors.New("turn is not steerable")

// parkQueuedTurnsTx stops the worker from claiming the turns still queued behind
// an interrupted one, and reports how many it parked.
//
// Scoped to 'queued' by design. A leased or running turn is being dealt with by
// the interrupt itself, and parking one would describe a turn that is executing
// as dormant — which is exactly what would make pendingTurnPredicate unsafe.
func parkQueuedTurnsTx(ctx context.Context, tx *sql.Tx, sessionID string) (int64, error) {
	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET auto_dispatch = 0
WHERE session_id = ?
  AND status = 'queued'
  AND auto_dispatch = 1`, sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReleaseParkedTurnParams identifies a parked turn the user wants to run now.
type ReleaseParkedTurnParams struct {
	SessionID string
	TurnID    int64
	Now       time.Time
}

// ReleaseParkedTurn hands one parked turn back to the worker.
//
// Only this turn is released. The user can pick any row, not just the head, so
// the others stay parked and ClaimNextTurn's `sequence ASC` then selects this one
// because it is the only eligible candidate — which is why releasing needs no
// priority column.
//
// The session returns to running_active: there is now work that will run, and
// EnqueueTurnMessage's guard is satisfied by the in-flight turn, so this does not
// block further input.
func (s *Store) ReleaseParkedTurn(ctx context.Context, p ReleaseParkedTurnParams) (SessionRuntimeSnapshot, error) {
	if strings.TrimSpace(p.SessionID) == "" {
		return SessionRuntimeSnapshot{}, fmt.Errorf("session id is required")
	}
	if p.TurnID <= 0 {
		return SessionRuntimeSnapshot{}, fmt.Errorf("turn id is required")
	}
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
SET auto_dispatch = 1
WHERE id = ?
  AND session_id = ?
  AND status = 'queued'
  AND auto_dispatch = 0
  AND NOT EXISTS(SELECT 1 FROM execution_steering q WHERE q.queued_turn_id=turns.id AND q.outcome='pending')`, p.TurnID, p.SessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	if affected != 1 {
		return SessionRuntimeSnapshot{}, ErrTurnNotReleasable
	}
	// The same accepting states EnqueueTurnMessage uses, and for the same reason:
	// releasing a parked turn is putting work in front of the agent. `paused`
	// belongs here because parked turns are excluded from the pending-turn
	// predicate, so idle release are free to reclaim the sandbox
	// while parked rows are still on screen — the caller wakes the session first,
	// and this is the transition that records it. A transitional state
	// (pausing/restoring) affects no row and reports not-releasable, so the
	// turn is never left released against a session that cannot run it.
	upd, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    last_activity_at = ?
WHERE id = ?
  AND status IN (?, ?, ?, ?)`,
		string(sessionstate.RunningActive), formatTime(p.Now), formatTime(p.Now), p.SessionID,
		string(sessionstate.Created), string(sessionstate.RunningIdle),
		string(sessionstate.Paused), string(sessionstate.RunningActive))
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	sessionAffected, err := upd.RowsAffected()
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	if sessionAffected != 1 {
		return SessionRuntimeSnapshot{}, ErrTurnNotReleasable
	}
	snapshot, err := getSessionRuntimeSnapshot(ctx, tx, p.SessionID)
	if err != nil {
		return SessionRuntimeSnapshot{}, err
	}
	return snapshot, tx.Commit()
}
