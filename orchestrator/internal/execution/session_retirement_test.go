package execution_test

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"testing"
	"time"
)

func TestRetirementRetriesJournalRecoveryBeforeProductFinalization(t *testing.T) {
	ctx := context.Background()
	original, st, backend, launcher, owner := controllerFixture(t)
	b, err := original.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, b.StoreID, b.SessionID, b.GenerationID, b.ProcessHandle, b.Epoch); err != nil {
		t.Fatal(err)
	}
	turn, err := st.EnqueueTurn(ctx, b.SessionID, "interrupted work", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bridge := st
	if _, ok, err := bridge.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if _, err := bridge.AckTurnStarted(ctx, store.AckStartedParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: owner, TurnID: turn}); err != nil {
		t.Fatal(err)
	}
	queued, err := st.EnqueueTurn(ctx, b.SessionID, "never dispatch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	reader := &retainedReader{store: st, turn: turn, fail: true}
	controller := &execution.Controller{Repository: st, Backend: backend, Routes: &recordingRevoker{}, RecoveryReader: reader, RecoveryProjector: &recoveryProjector{store: st}, Lifetime: ctx}
	defer controller.Close()
	if _, err := controller.RetireSession(ctx, b.SessionID, owner); err == nil {
		t.Fatal("retirement ignored failed replay")
	}
	if session, err := st.GetSession(ctx, b.SessionID); err != nil || session.Status == "destroyed" {
		t.Fatalf("retirement committed before replay: %+v %v", session, err)
	}
	if _, err := st.EnqueueTurn(ctx, b.SessionID, "late", time.Now()); err == nil {
		t.Fatal("failed retirement reopened admission")
	}
	if _, err := st.DBForTest().Exec(`UPDATE orchestrator_owner SET uuid='successor'`); err != nil {
		t.Fatal(err)
	}
	successor := store.GenerationLeaseOwner("successor")
	if _, err := controller.RetireSession(ctx, b.SessionID, owner); err == nil {
		t.Fatal("old owner continued retirement")
	}
	event, err := controller.RetireSession(ctx, b.SessionID, successor)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := controller.RetireSession(ctx, b.SessionID, successor); err != nil || retry != event {
		t.Fatalf("retirement retry changed result: %d %v", retry, err)
	}
	if status, _, err := st.TurnStatus(ctx, b.SessionID, queued); err != nil || status != "canceled" {
		t.Fatalf("queued input survived retirement: %s %v", status, err)
	}
	if status, _, err := st.TurnStatus(ctx, b.SessionID, turn); err != nil || status != "failed" {
		t.Fatalf("interrupted turn classification: %s %v", status, err)
	}
	if environment, err := st.GetEnvironmentBinding(ctx, b.EnvironmentID); err != nil || environment.SandboxID != b.SandboxID || environment.ObservedState != "paused" || environment.Epoch != b.Epoch+1 {
		t.Fatalf("retirement lost retained environment: %+v %v", environment, err)
	}
	if len(launcher.requests) != 1 || reader.calls != 2 {
		t.Fatalf("retirement restarted agent or skipped replay: starts=%d reads=%d", len(launcher.requests), reader.calls)
	}
	if _, err := controller.ReconnectIdle(ctx, b.SessionID, b.GenerationID, successor); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("retired session adopted: %v", err)
	}
}
