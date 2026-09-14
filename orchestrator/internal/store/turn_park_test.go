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
	if _, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID: sessionID, GenerationID: generationID, OwnerUUID: owner.UUID, Now: now.Add(time.Minute),
	}); err != nil {
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
	if _, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID: sessionID, GenerationID: generationID, OwnerUUID: owner.UUID, Now: now.Add(time.Minute),
	}); err != nil {
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
func TestRecordSteeredTurnRetiresTheRowAndWritesTheMessage(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_steer", "gen_steer"
	runningTurn, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "actually, do this")

	result, err := st.RecordSteeredTurn(ctx, RecordSteeredTurnParams{
		SessionID: sessionID, TurnID: queued[0], IntoTurnID: runningTurn, Now: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("record steered turn: %v", err)
	}
	if result.Message.Content != "actually, do this" || result.Message.Role != "user" {
		t.Fatalf("unexpected steered message: %+v", result.Message)
	}
	if result.Message.TurnID == nil || *result.Message.TurnID != queued[0] {
		t.Fatalf("steered message turn_id = %v, want %d", result.Message.TurnID, queued[0])
	}
	// The row is retired and points at where its content went, so a later reader can
	// explain why a 'completed' turn produced no output of its own.
	var status string
	var steeredInto *int64
	if err := st.db.QueryRowContext(ctx, `
SELECT status, steered_into_turn_id FROM turns WHERE id = ?`, queued[0]).Scan(&status, &steeredInto); err != nil {
		t.Fatalf("query steered turn: %v", err)
	}
	if status != "completed" || steeredInto == nil || *steeredInto != runningTurn {
		t.Fatalf("steered turn state: status=%q steered_into=%v", status, steeredInto)
	}
	// It leaves the queue: it will never be dispatched, so showing it as pending
	// would strand a row the user cannot act on.
	for _, turn := range result.Runtime.QueuedTurns {
		if turn.ID == queued[0] {
			t.Fatalf("steered turn is still in the queue: %+v", result.Runtime.QueuedTurns)
		}
	}
	// The running turn is untouched — that is the point of steer over abort: the
	// work already done survives.
	if result.Runtime.ActiveTurn == nil || result.Runtime.ActiveTurn.ID != runningTurn {
		t.Fatalf("steer disturbed the running turn: %+v", result.Runtime.ActiveTurn)
	}
	// Transcript order is the truth of what happened: the prompt the agent was
	// answering, then the steer the user sent mid-answer.
	msgs, err := st.ListMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Content != "actually, do this" {
		t.Fatalf("steered message is not last in the transcript: %+v", msgs)
	}
}

// Steering is the other thing a queue row can do, and the reason parking is not
// merely "cancel later": the content reaches the agent mid-answer, so the work
// already done survives instead of being thrown away and redone.
//
// The row is retired 'completed' rather than 'canceled'. The prompt genuinely ran
// — inside another turn — and a canceled row would tell the user it never did.
func TestRecordSteeredTurnRetiresTheRowAndWritesTheTranscript(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_steer", "gen_steer"
	runningTurn, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "actually, do this instead")

	result, err := st.RecordSteeredTurn(ctx, RecordSteeredTurnParams{
		SessionID: sessionID, TurnID: queued[0], IntoTurnID: runningTurn, Now: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("record steered turn: %v", err)
	}
	if result.Message.Content != "actually, do this instead" || result.Message.Role != "user" {
		t.Fatalf("unexpected steered message: %+v", result.Message)
	}
	// Gone from the queue: it will never be dispatched, so leaving it there would
	// show the user a prompt that is silently already delivered.
	for _, turn := range result.Runtime.QueuedTurns {
		if turn.ID == queued[0] {
			t.Fatalf("steered turn is still queued: %+v", result.Runtime.QueuedTurns)
		}
	}
	// The turn it was steered into is still running: that is what makes steer
	// different from stop-and-resend.
	if result.Runtime.ActiveTurn == nil || result.Runtime.ActiveTurn.ID != runningTurn {
		t.Fatalf("steer disturbed the running turn: %+v", result.Runtime.ActiveTurn)
	}
	var status string
	var into *int64
	if err := st.db.QueryRowContext(ctx, `
SELECT status, steered_into_turn_id FROM turns WHERE id = ?`, queued[0]).Scan(&status, &into); err != nil {
		t.Fatalf("query steered turn: %v", err)
	}
	if status != "completed" || into == nil || *into != runningTurn {
		t.Fatalf("steered row = status %q into %v, want completed into %d", status, into, runningTurn)
	}
	// Both prompts are in the transcript, the steered one after the turn it
	// interrupted — which is the order the user said them in.
	msgs, err := st.ListMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 || msgs[1].ID != result.Message.ID {
		t.Fatalf("transcript after steer: %+v", msgs)
	}
}

// The race this exists for: the turn ends between the host writing the steer
// command and this commit. The queued row must stay queued so the user can still
// release it as a turn of its own — retiring it against a finished turn would
// silently swallow the prompt.
func TestRecordSteeredTurnRefusesWhenTheTargetIsNoLongerRunning(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_steer_race", "gen_steer_race"
	runningTurn, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "too late")

	if _, err := st.db.ExecContext(ctx, `
UPDATE turns SET status = 'completed', completed_at = ? WHERE id = ?`,
		formatTime(now.Add(time.Second)), runningTurn); err != nil {
		t.Fatalf("complete running turn: %v", err)
	}
	if _, err := st.RecordSteeredTurn(ctx, RecordSteeredTurnParams{
		SessionID: sessionID, TurnID: queued[0], IntoTurnID: runningTurn, Now: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrTurnNotSteerable) {
		t.Fatalf("steer into a finished turn: err = %v, want ErrTurnNotSteerable", err)
	}
	var status string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, queued[0]).Scan(&status); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if status != "queued" {
		t.Fatalf("a failed steer consumed the prompt: status = %q, want queued", status)
	}
	// And nothing was written to the transcript for a delivery that did not happen.
	msgs, err := st.ListMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("failed steer wrote a message: %+v", msgs)
	}
}

// A steer whose source row is already gone (released and claimed, or steered
// twice by a double-click) must not write a second transcript entry.
func TestRecordSteeredTurnRefusesADuplicateDelivery(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_steer_dupe", "gen_steer_dupe"
	runningTurn, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "once")

	if _, err := st.RecordSteeredTurn(ctx, RecordSteeredTurnParams{
		SessionID: sessionID, TurnID: queued[0], IntoTurnID: runningTurn, Now: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("first steer: %v", err)
	}
	if _, err := st.RecordSteeredTurn(ctx, RecordSteeredTurnParams{
		SessionID: sessionID, TurnID: queued[0], IntoTurnID: runningTurn, Now: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrTurnNotSteerable) {
		t.Fatalf("second steer: err = %v, want ErrTurnNotSteerable", err)
	}
	msgs, err := st.ListMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("duplicate steer wrote another message: %+v", msgs)
	}
}

// A parked queue deliberately does not hold a sandbox open — it is excluded from
// the pending-turn predicate so auto-checkpoint and idle-release can reclaim
// compute while the rows wait on the user. So the session can be `checkpointed`
// by the time the user presses ↑, and releasing has to work from there: the
// caller wakes the session, and this transition records it. Rejecting it would
// make the button permanently dead and force the user to retype the prompt.
func TestReleaseParkedTurnWakesACheckpointedSession(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_release_cold", "gen_release_cold"
	_, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "still wanted")
	if _, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID: sessionID, GenerationID: generationID, OwnerUUID: owner.UUID, Now: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// What idle-release leaves behind: no live sandbox, the parked row untouched.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'checkpointed' WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("checkpoint session: %v", err)
	}

	snapshot, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{
		SessionID: sessionID, TurnID: queued[0], Now: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("release into a checkpointed session: %v", err)
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
func TestReleaseParkedTurnRejectsACheckpointingSession(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	sessionID, generationID := "sess_release_mid_ckpt", "gen_release_mid_ckpt"
	_, queued, now := parkedSessionFixture(t, ctx, st, owner, sessionID, generationID, "wait")
	if _, err := st.InterruptSession(ctx, InterruptSessionParams{
		SessionID: sessionID, GenerationID: generationID, OwnerUUID: owner.UUID, Now: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE sessions SET status = 'checkpointing' WHERE id = ?`, sessionID); err != nil {
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
