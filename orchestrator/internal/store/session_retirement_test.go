package store

import (
	"context"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"testing"
	"time"
)

func TestUnprovisionedSessionRetirementIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	leaseOwner := GenerationLeaseOwner(owner.UUID)
	createStoreSessionBackend(t, ctx, st, "unstarted", "sh", "khora")
	queued, err := st.EnqueueTurn(ctx, "unstarted", "park until deletion", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DestroySession(ctx, "unstarted", time.Now()); err == nil {
		t.Fatal("retained session entered legacy destruction")
	}
	if _, err := st.BeginSessionRetirement(ctx, "unstarted", leaseOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, "unstarted", "late", time.Now()); err == nil {
		t.Fatal("retiring session admitted input")
	}
	if _, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: "unstarted", Content: "late HTTP"}); err == nil {
		t.Fatal("retiring session admitted message")
	}
	if err := st.BeginWorkspaceMutation(ctx, workspace.Mutation{ID: "late-upload", SessionID: "unstarted", Owner: leaseOwner, Path: "file.txt"}); !errors.Is(err, workspace.ErrMutationBusy) {
		t.Fatalf("retiring session admitted upload: %v", err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER reject_retirement BEFORE INSERT ON events WHEN NEW.type='session.destroyed' BEGIN SELECT RAISE(ABORT,'injected final event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishSessionRetirement(ctx, "unstarted", leaseOwner); err == nil {
		t.Fatal("finalization ignored failed event")
	}
	if session, err := st.GetSession(ctx, "unstarted"); err != nil || session.Status == "destroyed" {
		t.Fatalf("failed finalization changed session: %+v %v", session, err)
	}
	if status, _, err := st.TurnStatus(ctx, "unstarted", queued); err != nil || status != "queued" {
		t.Fatalf("failed finalization partially canceled input: %s %v", status, err)
	}
	if _, err := st.db.Exec(`DROP TRIGGER reject_retirement`); err != nil {
		t.Fatal(err)
	}
	var event int64
	for range 2 {
		id, err := st.FinishSessionRetirement(ctx, "unstarted", leaseOwner)
		if err != nil {
			t.Fatal(err)
		}
		if event != 0 && event != id {
			t.Fatal("retirement retry duplicated event")
		}
		event = id
	}
	if status, _, err := st.TurnStatus(ctx, "unstarted", queued); err != nil || status != "canceled" {
		t.Fatalf("queued input not canceled: %s %v", status, err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileRetirement, leaseOwner, "", 16); err != nil || len(work) != 0 {
		t.Fatalf("complete retirement remained pending: %+v %v", work, err)
	}
}
func TestActiveSessionRetirementFencesAuthorityBeforeDrainAndRecovery(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, turn := activeAdoptionFixture(t)
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginSessionRetirement(ctx, b.SessionID, "foreign"); err == nil {
		t.Fatal("foreign owner requested retirement")
	}
	if _, err := st.BeginSessionRetirement(ctx, b.SessionID, b.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("retirement retained egress authority: %v", err)
	}
	if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, RequestID: "new", LeaseTTL: time.Minute}); err == nil {
		t.Fatal("retirement admitted claims")
	}
	if _, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute); err == nil {
		t.Fatal("retirement allowed adoption")
	}
	action, err := st.NextSessionRetirement(ctx, b.SessionID, b.Owner)
	if err != nil || action.Action != "recover" || action.GenerationID != b.GenerationID || len(action.Routes) != 1 {
		t.Fatalf("active retirement recovery action: %+v %v", action, err)
	}
	if _, err := st.FinishSessionRetirement(ctx, b.SessionID, b.Owner); err == nil {
		t.Fatal("active retirement finalized before writer termination")
	}
	if status, _, err := st.TurnStatus(ctx, b.SessionID, turn.TurnID); err != nil || status != "running" {
		t.Fatalf("retirement classified original work prematurely: %s %v", status, err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileRetirement, b.Owner, "", 16); err != nil || len(work) != 1 {
		t.Fatalf("retirement lost retry obligation: %+v %v", work, err)
	}
}
func TestRetirementPauseRetainsEnvironmentAndRequiresExactTerminationEvidence(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := boundExecutionFixture(t)
	// No process was started or accepted in this fixture, so pause alone can
	// settle its retained environment; there is no granted turn/journal to replay.
	if _, err := st.BeginSessionRetirement(ctx, b.SessionID, b.Owner); err != nil {
		t.Fatal(err)
	}
	action, err := st.NextSessionRetirement(ctx, b.SessionID, b.Owner)
	if err != nil || action.Action != "pause" {
		t.Fatalf("startup retirement pause: %+v %v", action, err)
	}
	if _, err := st.FinishSessionRetirement(ctx, b.SessionID, b.Owner); err == nil {
		t.Fatal("pending pause counted as termination")
	}
	observed := sandbox.Observation{Reference: sandbox.Reference{StoreID: b.StoreID, SandboxID: b.SandboxID, Epoch: b.Epoch, MutationRevision: "paused"}, State: "paused", PlacementState: "lost"}
	raw, _ := json.Marshal(observed)
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: action.Operation.ID, Fence: action.Operation.Fence, State: execution.OperationSucceeded, SandboxID: b.SandboxID, Epoch: b.Epoch, MutationRevision: "paused", Result: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='replacement'`); err != nil {
		t.Fatal(err)
	}
	owner := GenerationLeaseOwner("replacement")
	if _, err := st.FinishSessionRetirement(ctx, b.SessionID, b.Owner); err == nil {
		t.Fatal("old owner finished deletion")
	}
	if _, err := st.FinishSessionRetirement(ctx, b.SessionID, owner); err != nil {
		t.Fatal(err)
	}
	environment, err := st.GetEnvironmentBinding(ctx, b.EnvironmentID)
	if err != nil || environment.SandboxID != b.SandboxID || environment.Epoch != b.Epoch {
		t.Fatalf("product retirement destroyed storage identity: %+v %v", environment, err)
	}
	if session, err := st.GetSession(ctx, b.SessionID); err != nil || session.Status != "destroyed" || session.ActiveGenerationID != "" {
		t.Fatalf("retirement not terminal: %+v %v", session, err)
	}
}
