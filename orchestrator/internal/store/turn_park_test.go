package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// parkedSessionFixture builds the state a Stop leaves behind: one interrupted
// turn already retired, and `queuedContents` parked behind it.
func parkedSessionFixture(t *testing.T, ctx context.Context, st *Store, owner *OwnerLock, sessionID, generationID string, queuedContents ...string) (int64, []int64, time.Time) {
	t.Helper()
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, GenerationLeaseOwner(owner.UUID))
	now := time.Now().UTC()

	running, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: sessionID, Content: "long task", Now: now})
	if err != nil {
		t.Fatalf("enqueue running turn: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE turns
SET status = 'running', generation_id = ?, lease_owner = ?, lease_expires_at = ?, ack_started_at = ?
WHERE id = ?`, generationID, GenerationLeaseOwner(owner.UUID), formatTime(now.Add(time.Minute)), formatTime(now), running.TurnID); err != nil {
		t.Fatalf("set running turn: %v", err)
	}
	// Dispatching is what puts a prompt in the transcript, so do it for the turn the
	// agent is executing. The queued ones below are never dispatched, which is why a
	// steered turn has to write its own transcript entry.
	materializeForTest(t, ctx, st, sessionID, running.TurnID, now)
	var queued []int64
	for i, content := range queuedContents {
		result, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
			SessionID: sessionID,
			Content:   content,
			Now:       now.Add(time.Duration(i+1) * time.Second),
		})
		if err != nil {
			t.Fatalf("enqueue queued turn %d: %v", i, err)
		}
		queued = append(queued, result.TurnID)
	}
	return running.TurnID, queued, now
}

// Releasing is per-row, not "resume the queue". The user picks which parked
// prompt still matters, which is the whole point of parking rather than
// auto-continuing: after an interrupt the queue is usually stale, and running all
// of it is what Stop was pressed to prevent.
func TestReleaseParkedTurnReleasesOnlyThatTurn(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_release_one", "gen_release_one"
	_, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "first", "second")
	if _, _, err := st.RequestBoundInterrupt(ctx, sessionID, generationID, GenerationLeaseOwner(owner.UUID)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	// Release the second, not the head: any row is releasable, and ClaimNextTurn's
	// `sequence ASC` must not drag the still-parked first one along.
	snapshot, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[1], Now: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("release parked turn: %v", err)
	}
	if snapshot.SessionStatus != "running_active" {
		t.Fatalf("session status = %q, want running_active", snapshot.SessionStatus)
	}
	parked := map[int64]bool{}
	for _, turn := range snapshot.QueuedTurns {
		parked[turn.ID] = turn.Parked
	}
	if len(parked) != 2 || !parked[queued[0]] || parked[queued[1]] {
		t.Fatalf("release changed the wrong rows: %+v", snapshot.QueuedTurns)
	}
}

// Every rejection here is a click racing a state change, not a bug: the button
// was rendered from a snapshot that has since moved on. They must fail cleanly as
// ErrTurnNotReleasable so the caller answers 409 and the client re-reads, rather
// than surfacing a 500.
func TestReleaseParkedTurnRejectsTurnsItMustNotTouch(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_release_reject", "gen_release_reject"
	runningTurn, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "first")

	// Not parked yet: a double-click that beat the interrupt. Releasing an already
	// live turn would be a silent no-op that looks like success.
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[0], Now: now,
	}); !errors.Is(err, ErrTurnNotReleasable) {
		t.Fatalf("release of an unparked turn: err = %v, want ErrTurnNotReleasable", err)
	}
	if _, _, err := st.RequestBoundInterrupt(ctx, sessionID, generationID, GenerationLeaseOwner(owner.UUID)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// The interrupted turn is terminal, and a session-scoped id from another
	// session must not be reachable — the endpoint authorizes the session, so the
	// turn lookup is what keeps one session from releasing another's work.
	for label, turnID := range map[string]int64{
		"the interrupted turn": runningTurn,
		"an unknown turn":      queued[0] + 9999,
	} {
		if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
			SessionID: sessionID, TurnID: turnID, Now: now.Add(2 * time.Minute),
		}); !errors.Is(err, ErrTurnNotReleasable) {
			t.Fatalf("release of %s: err = %v, want ErrTurnNotReleasable", label, err)
		}
	}
	// Releasing twice is the same race one step later.
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[0], Now: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[0], Now: now.Add(4 * time.Minute),
	}); !errors.Is(err, ErrTurnNotReleasable) {
		t.Fatalf("second release: err = %v, want ErrTurnNotReleasable", err)
	}
}

// Steering is the other thing a queued prompt can become: instead of waiting for
// its own turn, its content is injected into the turn already running. The row
// therefore never executes — but its content did reach the agent, so retiring it
// as 'completed' with a transcript entry is what tells the truth. A 'canceled' row
// would claim the prompt never ran.

// Steering is the other thing a queue row can do, and the reason parking is not
// merely "cancel later": the content reaches the agent mid-answer, so the work
// already done survives instead of being thrown away and redone.
//
// The row is retired 'completed' rather than 'canceled'. The prompt genuinely ran
// — inside another turn — and a canceled row would tell the user it never did.

// The race this exists for: the turn ends between the host writing the steer
// command and this commit. The queued row must stay queued so the user can still
// release it as a turn of its own — retiring it against a finished turn would
// silently swallow the prompt.

// A steer whose source row is already gone (released and claimed, or steered
// twice by a double-click) must not write a second transcript entry.

// A parked queue deliberately does not hold a sandbox open — it is excluded from
// the pending-turn predicate so auto-checkpoint and idle-release can reclaim
// compute while the rows wait on the user. So the session can be `paused`
// by the time the user presses ↑, and releasing has to work from there: the
// caller wakes the session, and this transition records it. Rejecting it would
// make the button permanently dead and force the user to retype the prompt.
func TestReleaseParkedTurnWakesAPausedSession(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_release_cold", "gen_release_cold"
	_, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "still wanted")
	if _, _, err := st.RequestBoundInterrupt(ctx, sessionID, generationID, GenerationLeaseOwner(owner.UUID)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// What idle-release leaves behind: no live sandbox, the parked row untouched.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'paused' WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("checkpoint session: %v", err)
	}

	snapshot, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[0], Now: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("release into a paused session: %v", err)
	}
	if snapshot.SessionStatus != "running_active" {
		t.Fatalf("session status = %q, want running_active", snapshot.SessionStatus)
	}
	if len(snapshot.QueuedTurns) != 1 || snapshot.QueuedTurns[0].Parked {
		t.Fatalf("turn not released: %+v", snapshot.QueuedTurns)
	}
}

// Mid-checkpoint is the state where the session's own transition owns the status
// (restoring is a generation status, not a session one). Releasing there must
// change nothing rather than half-apply: the turn stays parked and the caller
// answers 409, so the user's prompt is still there to send once it settles.
func TestReleaseParkedTurnRejectsAPausingSession(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_release_mid_ckpt", "gen_release_mid_ckpt"
	_, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "wait")
	if _, _, err := st.RequestBoundInterrupt(ctx, sessionID, generationID, GenerationLeaseOwner(owner.UUID)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'pausing' WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[0], Now: now.Add(2 * time.Minute),
	}); !errors.Is(err, ErrTurnNotReleasable) {
		t.Fatalf("err = %v, want ErrTurnNotReleasable", err)
	}
	// The rollback is the point: a released row against a session that cannot run
	// it would strand the prompt.
	var autoDispatch int
	if err := st.db.QueryRowContext(ctx,
		`SELECT auto_dispatch FROM turns WHERE id = ?`, queued[0]).Scan(&autoDispatch); err != nil {
		t.Fatalf("read auto_dispatch: %v", err)
	}
	if autoDispatch != 0 {
		t.Fatalf("turn was released despite the rejection")
	}
}
