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
		TerminalStatus: "completed", Now: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	if _, err := st.ResumeFailedTurn(ctx, ResumeFailedTurnParams{
		SessionID: "sess_r", TurnID: turnID, Content: "carry on", MaxAttempts: 2,
	}); !errors.Is(err, ErrTurnNotResumable) {
		t.Fatalf("expected ErrTurnNotResumable for a completed turn, got %v", err)
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
