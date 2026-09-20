package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

// failTurn drives a turn to terminal 'failed' the way the bridge does, so these
// tests exercise the real precondition rather than a hand-written row.
func failTurn(t *testing.T, ctx context.Context, st *Store, sessionID, generationID, owner string, turnID int64, errorClass string) {
	t.Helper()
	now := time.Now().UTC()
	if _, _, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		Owner:        owner,
		RequestID:    fmt.Sprintf("req-fail-%s-%d", errorClass, turnID),
		LeaseTTL:     time.Minute,
		Now:          now,
	}); err != nil {
		t.Fatalf("claim before fail: %v", err)
	}
	if _, err := st.CompleteTurn(ctx, CompleteTurnParams{
		SessionID:      sessionID,
		GenerationID:   generationID,
		TurnID:         turnID,
		Owner:          owner,
		TerminalStatus: "failed",
		ErrorClass:     errorClass,
		Error:          errorClass,
		Now:            now.Add(time.Second),
	}); err != nil {
		t.Fatalf("complete turn as failed: %v", err)
	}
}

func resumeTestStore(t *testing.T, sessionID, generationID, owner string) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	createStoreSession(t, ctx, st, sessionID)
	createActiveGeneration(t, ctx, st, sessionID, generationID, owner)
	return st, ctx
}

func TestResumeFailedTurnAppendsContinuation(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original work", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failTurn(t, ctx, st, "sess_r", "gen_r", "owner", turnID, "upstream_rate_limit")

	result, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID:   "sess_r",
		TurnID:      turnID,
		Content:     "carry on",
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.TurnID == turnID {
		t.Fatal("resume must append a new turn, not reuse the failed one")
	}
	if result.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", result.Attempt)
	}

	// The failed turn stays terminal: re-dispatching a turn that already ran tool
	// calls could repeat their side effects.
	var status string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, turnID).Scan(&status); err != nil {
		t.Fatalf("read failed turn: %v", err)
	}
	if status != "failed" {
		t.Fatalf("original turn status = %q, want failed", status)
	}

	// The continuation is queued and marked so its depth can be bounded.
	var newStatus, retryPolicy string
	if err := st.db.QueryRowContext(ctx,
		`SELECT status, COALESCE(retry_policy, '') FROM turns WHERE id = ?`, result.TurnID,
	).Scan(&newStatus, &retryPolicy); err != nil {
		t.Fatalf("read resume turn: %v", err)
	}
	if newStatus != "queued" {
		t.Fatalf("resume turn status = %q, want queued", newStatus)
	}
	if retryPolicy != "resume:1" {
		t.Fatalf("retry_policy = %q, want resume:1", retryPolicy)
	}

	// Session is flipped back to running so the bridge dispatches the turn.
	session, err := st.GetSession(ctx, "sess_r")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Status != string(sessionstate.RunningActive) {
		t.Fatalf("session status = %q, want running_active", session.Status)
	}
}

// The continuation is machine-authored. Writing a messages row would show the
// user a prompt they never typed.
func TestResumeFailedTurnWritesNoUserMessage(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failTurn(t, ctx, st, "sess_r", "gen_r", "owner", turnID, "upstream_rate_limit")

	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_r", TurnID: turnID, Content: "carry on", MaxAttempts: 2,
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	messages, err := st.ListMessages(ctx, "sess_r")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, message := range messages {
		if message.Content == "carry on" {
			t.Fatal("resume must not write the continuation prompt as a user message")
		}
	}
}

// A persistent fault must not walk the session forward one continuation at a
// time, so resuming a resume increments the same budget.
func TestResumeFailedTurnBoundsContinuationDepth(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	current := turnID
	for attempt := 1; attempt <= 2; attempt++ {
		failTurn(t, ctx, st, "sess_r", "gen_r", "owner", current, "upstream_rate_limit")
		result, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
			SessionID: "sess_r", TurnID: current, Content: "carry on", MaxAttempts: 2,
		})
		if err != nil {
			t.Fatalf("resume %d: %v", attempt, err)
		}
		if result.Attempt != attempt {
			t.Fatalf("attempt = %d, want %d", result.Attempt, attempt)
		}
		current = result.TurnID
	}

	// Third continuation exceeds MaxAttempts=2 and must be refused.
	failTurn(t, ctx, st, "sess_r", "gen_r", "owner", current, "upstream_rate_limit")
	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_r", TurnID: current, Content: "carry on", MaxAttempts: 2,
	}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("expected ErrTurnNotResumable past the budget, got %v", err)
	}
}

// A user follow-up outranks an automatic continuation, and double-driving the
// session would run two turns at once.
func TestResumeFailedTurnDeclinesWhenWorkIsInFlight(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failTurn(t, ctx, st, "sess_r", "gen_r", "owner", turnID, "upstream_rate_limit")

	if _, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: "sess_r",
		Content:   "user asked something else",
		Now:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("enqueue user follow-up: %v", err)
	}

	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_r", TurnID: turnID, Content: "carry on", MaxAttempts: 2,
	}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("expected ErrTurnNotResumable with a queued turn, got %v", err)
	}
}

// Only a terminally failed turn is continued. Completing or cancelling one is a
// legitimate end state that must not be reopened behind the user's back.
func TestResumeFailedTurnRequiresFailedTurn(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	now := time.Now().UTC()
	if _, _, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID: "sess_r", GenerationID: "gen_r", Owner: "owner",
		RequestID: "req-ok", LeaseTTL: time.Minute, Now: now,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.CompleteTurn(ctx, CompleteTurnParams{
		SessionID: "sess_r", GenerationID: "gen_r", TurnID: turnID, Owner: "owner",
		TerminalStatus: "canceled", Now: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_r", TurnID: turnID, Content: "carry on", MaxAttempts: 2,
	}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("expected ErrTurnNotResumable for a canceled turn, got %v", err)
	}
}

// Cross-session isolation: a turn id from another session must never be resumed
// into this one.
func TestResumeFailedTurnRejectsForeignTurn(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	createStoreSession(t, ctx, st, "sess_other")

	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failTurn(t, ctx, st, "sess_r", "gen_r", "owner", turnID, "upstream_rate_limit")

	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_other", TurnID: turnID, Content: "carry on", MaxAttempts: 2,
	}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("expected ErrTurnNotResumable for a foreign turn, got %v", err)
	}
}

// Without a live generation the continuation would sit queued against a dead
// sandbox, leaving the session stuck rather than merely truncated.
func TestResumeFailedTurnRequiresLiveGeneration(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_r", "gen_r", "owner")
	turnID, err := st.EnqueueTurn(ctx, "sess_r", "original", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failTurn(t, ctx, st, "sess_r", "gen_r", "owner", turnID, "upstream_rate_limit")

	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtime_generations SET status = 'destroyed' WHERE generation_id = ?`, "gen_r",
	); err != nil {
		t.Fatalf("retire generation: %v", err)
	}

	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_r", TurnID: turnID, Content: "carry on", MaxAttempts: 2,
	}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("expected ErrTurnNotResumable without a live generation, got %v", err)
	}
}

func TestResumeAttemptNumber(t *testing.T) {
	cases := []struct {
		name   string
		policy sql.NullString
		want   int
	}{
		{name: "user-authored turn has no policy", policy: sql.NullString{}, want: 1},
		{name: "empty policy", policy: sql.NullString{String: "", Valid: true}, want: 1},
		{name: "first continuation", policy: sql.NullString{String: "resume:1", Valid: true}, want: 2},
		{name: "third continuation", policy: sql.NullString{String: "resume:3", Valid: true}, want: 4},
		{name: "unrelated policy is not ours", policy: sql.NullString{String: "backoff:2", Valid: true}, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resumeAttemptNumber(tc.policy)
			if err != nil {
				t.Fatalf("resumeAttemptNumber(%+v): %v", tc.policy, err)
			}
			if got != tc.want {
				t.Fatalf("resumeAttemptNumber(%+v) = %d, want %d", tc.policy, got, tc.want)
			}
		})
	}
}

func TestGetSessionRuntimeSnapshotRestoresActiveAndQueuedTurns(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_snapshot", "gen_snapshot", "owner")
	now := time.Now().UTC().Truncate(time.Second)
	first, err := st.EnqueueTurn(ctx, "sess_snapshot", "active work", now)
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	second, err := st.EnqueueTurn(ctx, "sess_snapshot", "next work", now.Add(time.Second))
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	started := now.Add(2 * time.Second)
	if _, err := st.db.ExecContext(ctx, `UPDATE turns SET status='running', generation_id=?, started_at=?, ack_started_at=? WHERE id=?`, "gen_snapshot", formatTime(started), formatTime(started), first); err != nil {
		t.Fatalf("mark active: %v", err)
	}
	snapshot, err := st.GetSessionRuntimeSnapshot(ctx, "sess_snapshot")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.SessionID != "sess_snapshot" || snapshot.ActiveTurn == nil {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.ActiveTurn.ID != first || snapshot.ActiveTurn.Status != "running" {
		t.Fatalf("active turn: %+v", snapshot.ActiveTurn)
	}
	if len(snapshot.QueuedTurns) != 1 || snapshot.QueuedTurns[0].ID != second {
		t.Fatalf("queued turns: %+v", snapshot.QueuedTurns)
	}
	// A running turn that has written no phase frame yet still reads as active
	// work, so the strip is never blank during bridge handoff.
	if snapshot.ActiveTurn.Phase != TurnPhaseThinking {
		t.Fatalf("active phase = %q, want thinking", snapshot.ActiveTurn.Phase)
	}
	// A queued turn has not run, so it carries no activity of its own.
	if snapshot.QueuedTurns[0].Phase != TurnPhaseNone {
		t.Fatalf("queued turn carries a phase: %+v", snapshot.QueuedTurns[0])
	}
}

// Phase is read from the turn row rather than reconstructed from the newest
// event. That distinction is the whole point: for a turn running a tool the
// newest durable event is almost always an agent.delta, so the old
// event-derived projection reported "streaming" while a tool was executing.
func TestSetTurnPhaseProjectsToolActivity(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_phase", "gen_phase", "owner")
	now := time.Now().UTC().Truncate(time.Second)
	turnID, err := st.EnqueueTurn(ctx, "sess_phase", "run the tool", now)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE turns SET status='running', generation_id=?, started_at=?, ack_started_at=? WHERE id=?`,
		"gen_phase", formatTime(now), formatTime(now), turnID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	before, err := st.GetSessionRuntimeSnapshot(ctx, "sess_phase")
	if err != nil {
		t.Fatalf("snapshot before: %v", err)
	}

	snapshot, err := st.SetTurnPhase(ctx, SetTurnPhaseParams{
		SessionID: "sess_phase", TurnID: turnID, Phase: TurnPhaseTool,
		ToolName: "bash", ToolDetail: "go test ./...", Now: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("set phase: %v", err)
	}
	if snapshot.ActiveTurn == nil || snapshot.ActiveTurn.Phase != TurnPhaseTool {
		t.Fatalf("active turn phase: %+v", snapshot.ActiveTurn)
	}
	if snapshot.ActiveTurn.ToolName != "bash" || snapshot.ActiveTurn.ToolDetail != "go test ./..." {
		t.Fatalf("tool projection: %+v", snapshot.ActiveTurn)
	}
	if snapshot.Revision <= before.Revision {
		t.Fatalf("revision did not advance: %d -> %d", before.Revision, snapshot.Revision)
	}

	// A repeated identical frame must not advance the revision, or every client
	// would drop its cached snapshot for a no-op.
	repeat, err := st.SetTurnPhase(ctx, SetTurnPhaseParams{
		SessionID: "sess_phase", TurnID: turnID, Phase: TurnPhaseTool,
		ToolName: "bash", ToolDetail: "go test ./...", Now: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("repeat phase: %v", err)
	}
	if repeat.Revision != snapshot.Revision {
		t.Fatalf("redundant frame advanced revision: %d -> %d", snapshot.Revision, repeat.Revision)
	}
}

// A phase frame that arrives after the turn ended must not reanimate it. The
// bridge is asynchronous, so this ordering happens routinely on cancel.
func TestSetTurnPhaseIgnoresTerminalTurn(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_phase_late", "gen_phase_late", "owner")
	now := time.Now().UTC().Truncate(time.Second)
	turnID, err := st.EnqueueTurn(ctx, "sess_phase_late", "already done", now)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE turns SET status='completed', completed_at=? WHERE id=?`,
		formatTime(now), turnID); err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	snapshot, err := st.SetTurnPhase(ctx, SetTurnPhaseParams{
		SessionID: "sess_phase_late", TurnID: turnID, Phase: TurnPhaseStreaming, Now: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("set phase: %v", err)
	}
	if snapshot.ActiveTurn != nil {
		t.Fatalf("terminal turn became active again: %+v", snapshot.ActiveTurn)
	}
}

// A session may remain marked running_active while its generation is warming up
// or just after a turn completed. "Busy" is defined by an active turn, not by
// the session status, so such a session must hydrate as idle — this is the
// invariant behind the reported bug where an idle session displayed Thinking.
func TestGetSessionRuntimeSnapshotIdleWhenRunningSessionHasNoTurns(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_snapshot_idle", "gen_snapshot_idle", "owner")
	// The exact shape of the reported bug: the session claims to be running while
	// no turn exists, which is what a warming generation looks like.
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET status=? WHERE id=?`,
		string(sessionstate.RunningActive), "sess_snapshot_idle"); err != nil {
		t.Fatalf("mark running_active: %v", err)
	}
	snapshot, err := st.GetSessionRuntimeSnapshot(ctx, "sess_snapshot_idle")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.SessionStatus != string(sessionstate.RunningActive) {
		t.Fatalf("session status = %q, want running_active", snapshot.SessionStatus)
	}
	if snapshot.ActiveTurn != nil || len(snapshot.QueuedTurns) != 0 {
		t.Fatalf("running_active session reported work with no turns: %+v", snapshot)
	}
}

// Withdrawing a queued turn must stop it from ever executing and remove its
// message: the turn was never asked, so leaving the text in the transcript would
// read as an unanswered prompt on the next load.
func TestCancelQueuedTurnRemovesTurnAndMessage(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_cancel", "gen_cancel", "owner")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET status=? WHERE id=?`,
		string(sessionstate.RunningIdle), "sess_cancel"); err != nil {
		t.Fatalf("reset status: %v", err)
	}
	first, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: "sess_cancel", Content: "running work", Now: now})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE turns SET status='running', generation_id=?, started_at=?, ack_started_at=? WHERE id=?`,
		"gen_cancel", formatTime(now), formatTime(now), first.TurnID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	firstMessage := materializeForTest(t, ctx, st, "sess_cancel", first.TurnID, now)
	second, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: "sess_cancel", Content: "withdraw me", Now: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	snapshot, err := st.CancelQueuedTurn(ctx, CancelQueuedTurnParams{SessionID: "sess_cancel", TurnID: second.TurnID, Now: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	if len(snapshot.QueuedTurns) != 0 {
		t.Fatalf("queue not drained: %+v", snapshot.QueuedTurns)
	}
	if snapshot.ActiveTurn == nil || snapshot.ActiveTurn.ID != first.TurnID {
		t.Fatalf("cancel disturbed the active turn: %+v", snapshot.ActiveTurn)
	}
	if snapshot.Revision <= second.Runtime.Revision {
		t.Fatalf("revision did not advance: %d -> %d", second.Runtime.Revision, snapshot.Revision)
	}
	msgs, err := st.ListMessages(ctx, "sess_cancel")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != firstMessage.ID {
		t.Fatalf("withdrawn turn left a transcript entry: %+v", msgs)
	}

	// Racing the worker's claim is an expected outcome, reported rather than
	// silently ignored.
	if _, err := st.CancelQueuedTurn(ctx, CancelQueuedTurnParams{SessionID: "sess_cancel", TurnID: first.TurnID, Now: now.Add(3 * time.Second)}); !errors.Is(err, ErrTurnNotCancelable) {
		t.Fatalf("cancel of a running turn: got %v, want ErrTurnNotCancelable", err)
	}
}

// The transcript timestamp must be when the prompt reached the agent, not when it
// was typed. AckTurnStarted is the hook: the bridge sends it as the agent begins
// the turn, immediately before it builds its first model request.
func TestAckTurnStartedMaterializesMessageAtDispatchTime(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_dispatch", "gen_dispatch", "owner")
	// The fixture's generation lease runs one minute from real time, so the queue
	// wait is simulated backwards: the prompt is "typed" three minutes ago and
	// dispatched now. What matters is that enqueue and dispatch differ.
	dispatchedAt := time.Now().UTC().Truncate(time.Second)
	typedAt := dispatchedAt.Add(-3 * time.Minute)
	if _, err := st.db.ExecContext(ctx, `UPDATE sessions SET status=? WHERE id=?`,
		string(sessionstate.RunningIdle), "sess_dispatch"); err != nil {
		t.Fatalf("reset status: %v", err)
	}
	enq, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: "sess_dispatch", Content: "queued for a while", Now: typedAt,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Nothing in the transcript: the prompt has not been asked.
	msgs, err := st.ListMessages(ctx, "sess_dispatch")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("queued turn wrote a transcript entry: %+v", msgs)
	}

	// The turn waits in the queue, then the bridge claims and acks it.
	if _, _, err := st.ClaimNextTurn(ctx, ClaimNextTurnParams{
		SessionID: "sess_dispatch", GenerationID: "gen_dispatch", Owner: "owner",
		RequestID: "req_dispatch", LeaseTTL: time.Minute, Now: dispatchedAt,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.AckTurnStarted(ctx, AckStartedParams{
		SessionID: "sess_dispatch", GenerationID: "gen_dispatch", TurnID: enq.TurnID,
		Owner: "owner", SandboxSourceIP: "10.240.0.2", LeaseTTL: time.Minute, Now: dispatchedAt,
	}); err != nil {
		t.Fatalf("ack started: %v", err)
	}

	msgs, err = st.ListMessages(ctx, "sess_dispatch")
	if err != nil {
		t.Fatalf("list after dispatch: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "queued for a while" {
		t.Fatalf("dispatched turn missing from transcript: %+v", msgs)
	}
	// The whole point: the prompt sat queued for three minutes, and the transcript
	// says it was asked at dispatch — not when the user pressed send.
	if !msgs[0].CreatedAt.Equal(dispatchedAt) {
		t.Fatalf("message created_at = %s, want dispatch time %s (typed at %s)",
			msgs[0].CreatedAt, dispatchedAt, typedAt)
	}
	if msgs[0].TurnID == nil || *msgs[0].TurnID != enq.TurnID {
		t.Fatalf("message not linked to its turn: %+v", msgs[0])
	}

	// A replayed ack must not append the prompt twice.
	if _, err := st.AckTurnStarted(ctx, AckStartedParams{
		SessionID: "sess_dispatch", GenerationID: "gen_dispatch", TurnID: enq.TurnID,
		Owner: "owner", SandboxSourceIP: "10.240.0.2", LeaseTTL: time.Minute, Now: dispatchedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("replayed ack: %v", err)
	}
	msgs, err = st.ListMessages(ctx, "sess_dispatch")
	if err != nil {
		t.Fatalf("list after replay: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("replayed ack duplicated the prompt: %+v", msgs)
	}
}

// An automatic continuation is not the user's message. ResumeFailedTurn leaves
// materializes_message unset, so dispatching it must add nothing to the transcript.
func TestResumeTurnDispatchWritesNoUserMessage(t *testing.T) {
	st, ctx := resumeTestStore(t, "sess_resume_msg", "gen_resume_msg", "owner")
	now := time.Now().UTC().Truncate(time.Second)
	turnID, err := st.EnqueueTurn(ctx, "sess_resume_msg", "original work", now)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failTurn(t, ctx, st, "sess_resume_msg", "gen_resume_msg", "owner", turnID, "upstream_rate_limit")
	resumed, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_resume_msg", TurnID: turnID, Content: "carry on", MaxAttempts: 2, Now: now,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, ok, err := materializeTurnMessage(t, ctx, st, "sess_resume_msg", resumed.TurnID, now); err != nil || ok {
		t.Fatalf("continuation produced a user message: ok=%v err=%v", ok, err)
	}
	msgs, err := st.ListMessages(ctx, "sess_resume_msg")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, m := range msgs {
		if m.Content == "carry on" {
			t.Fatalf("synthetic continuation shown as the user's message: %+v", msgs)
		}
	}
}
