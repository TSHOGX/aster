package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/store"
)

type resumeRecorder struct {
	serverStore
	candidates []store.TurnContinuation
	calls      []store.ResumeFailedTurnParams
	after      string
	cutoff     time.Time
	err        error
}

func (r *resumeRecorder) PendingTurnContinuations(_ context.Context, after string, cutoff time.Time, _ int) ([]store.TurnContinuation, error) {
	r.after = after
	r.cutoff = cutoff
	return r.candidates, nil
}
func (r *resumeRecorder) ResumeFailedTurn(_ context.Context, p store.ResumeFailedTurnParams) (store.ResumeFailedTurnResult, error) {
	r.calls = append(r.calls, p)
	return store.ResumeFailedTurnResult{TurnID: 99, Attempt: 1}, r.err
}
func TestContinuationMaintenanceUsesDurableCandidatesAndConfiguredBudget(t *testing.T) {
	recorder := &resumeRecorder{candidates: []store.TurnContinuation{{SessionID: "a", TurnID: 7}, {SessionID: "b", TurnID: 8}}}
	srv := &Server{store: recorder, hub: events.NewHub(), log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config.Config{Harness: config.HarnessConfig{Bridge: config.BridgeConfig{TurnResume: config.TurnResumeConfig{Enabled: true, MaxAttempts: 2, Backoff: config.Duration{Duration: time.Second}}}}}}
	before := time.Now()
	if err := srv.resumeDueTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 2 || recorder.calls[0].Content != resumeContinuationPrompt || recorder.calls[0].MaxAttempts != 2 || recorder.cutoff.After(before) || srv.continuationAfter != "b" {
		t.Fatalf("continuation bounds: %+v", recorder)
	}
	recorder.candidates = nil
	if err := srv.resumeDueTurns(context.Background()); err != nil || recorder.after != "b" || srv.continuationAfter != "" {
		t.Fatalf("continuation cursor: %v", err)
	}
	recorder.candidates = []store.TurnContinuation{{SessionID: "a", TurnID: 7}}
	recorder.err = store.ErrTurnNotResumable
	if err := srv.resumeDueTurns(context.Background()); err != nil {
		t.Fatal("declined continuation failed maintenance")
	}
	srv.cfg.Harness.Bridge.TurnResume.Enabled = false
	recorder.calls = nil
	if err := srv.resumeDueTurns(context.Background()); err != nil || len(recorder.calls) != 0 {
		t.Fatal("disabled continuation dispatched work")
	}
}
