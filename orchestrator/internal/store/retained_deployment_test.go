package store

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"testing"
	"time"
)

func TestRetainedOnlyDeploymentRequiresExplicitSessionMigration(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	if err := st.RequireRetainedDeployment(ctx, GenerationLeaseOwner(owner.UUID)); err != nil {
		t.Fatal(err)
	}
	createStoreSessionBackend(t, ctx, st, "legacy-session", "sh", "legacy")
	if err := st.RequireRetainedDeployment(ctx, GenerationLeaseOwner(owner.UUID)); err == nil {
		t.Fatal("retained startup silently adopted legacy sessions")
	}
	if backend, err := st.SessionExecutionBackend(ctx, "legacy-session"); err != nil || backend != "legacy" {
		t.Fatalf("startup changed persisted backend: %s %v", backend, err)
	}
}

func TestRetainedIdleLoopResumesPauseAfterOwnerReplacement(t *testing.T) {
	ctx := context.Background()
	st, _, b, observed := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	release, err := st.BeginIdleExecutionRelease(ctx, b.GenerationID, b.Owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	paused := sandbox.Observation{Reference: sandbox.Reference{StoreID: b.StoreID, SandboxID: b.SandboxID, Epoch: b.Epoch, MutationRevision: "pause-completed"}, State: "paused", PlacementState: "lost"}
	raw, _ := json.Marshal(paused)
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: release.Operation.ID, Fence: release.Operation.Fence, State: execution.OperationSucceeded, SandboxID: b.SandboxID, Epoch: b.Epoch, MutationRevision: paused.MutationRevision, Result: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='new-owner'`); err != nil {
		t.Fatal(err)
	}
	owner := GenerationLeaseOwner("new-owner")
	candidates, err := st.RetainedIdleCandidates(ctx, owner, "", time.Now().Add(-time.Hour), 16)
	if err != nil || len(candidates) != 1 || candidates[0].GenerationID != b.GenerationID {
		t.Fatalf("lost pause completion was not selected: %+v %v", candidates, err)
	}
	if err := st.FinishIdleExecutionRelease(ctx, b.GenerationID, owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	if candidates, err := st.RetainedIdleCandidates(ctx, owner, "", time.Now(), 16); err != nil || len(candidates) != 0 {
		t.Fatalf("settled release selected again: %+v %v", candidates, err)
	}
}
func TestRetainedIdleCandidatesRequireIdleWithoutQueuedWork(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, obs := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if candidates, err := st.RetainedIdleCandidates(ctx, b.Owner, "", time.Now().Add(time.Second), 16); err != nil || len(candidates) != 1 {
		t.Fatalf("idle environment missing: %+v %v", candidates, err)
	}
	queued, err := st.EnqueueTurn(ctx, b.SessionID, "queued", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := st.RetainedIdleCandidates(ctx, b.Owner, "", time.Now().Add(time.Second), 16); err != nil || len(candidates) != 0 {
		t.Fatalf("queued environment selected for pause: %+v %v", candidates, err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: queued, TerminalStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	if candidates, err := st.RetainedIdleCandidates(ctx, b.Owner, b.GenerationID, time.Now().Add(time.Second), 16); err != nil || len(candidates) != 0 {
		t.Fatalf("idle keyset cursor did not advance: %+v %v", candidates, err)
	}
	if _, err := st.RetainedIdleCandidates(ctx, "foreign", "", time.Now(), 16); err == nil {
		t.Fatal("foreign owner enumerated idle work")
	}
}
