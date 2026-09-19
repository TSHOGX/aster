package store

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"testing"
	"time"
)

func TestEngineResyncAtomicallyFencesKnownEnvironmentMissingFromRetainedEvents(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := activeAdoptionFixture(t)
	if _, err := st.db.Exec(`CREATE TRIGGER reject_gap BEFORE INSERT ON environment_reconcile_requests BEGIN SELECT RAISE(ABORT,'injected gap fence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginEngineResync(ctx, b.StoreID, b.Owner, 0, 4, 7); err == nil {
		t.Fatal("resync ignored invalidation failure")
	}
	cursor, err := st.EngineCursor(ctx, b.StoreID)
	if err != nil || cursor.After != 0 || cursor.Resync {
		t.Fatalf("failed invalidation partially moved cursor: %+v %v", cursor, err)
	}
	if err := st.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err != nil {
		t.Fatal("rolled-back resync changed readiness")
	}
	if _, err := st.db.Exec(`DROP TRIGGER reject_gap`); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginEngineResync(ctx, b.StoreID, b.Owner, 0, 4, 7); err != nil {
		t.Fatal(err)
	}
	if sequence, err := st.EnvironmentInvalidationSequence(ctx, b.EnvironmentID); err != nil || sequence != 7 {
		t.Fatalf("known environment missing from retained window was not fenced: %d %v", sequence, err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("gap retained gateway authority: %v", err)
	}
	a, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observation := execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: b.Owner, ProcessHandle: b.ProcessHandle, Epoch: b.Epoch, EnvironmentFence: b.EnvironmentFence, ReconciledEventSequence: &a.EventSequence}
	if err := st.AcceptExecutionBinding(ctx, observation); err == nil {
		t.Fatal("handshake restored readiness before cursor catchup")
	}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, b.Owner, 4, []sandbox.Event{{ID: "retained-other", Sequence: 7, SandboxID: "unrelated", Kind: "created", Epoch: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err == nil {
		t.Fatal("cursor position alone bypassed unfinished resync")
	}
	if err := st.FinishEngineResync(ctx, b.StoreID, b.Owner, 7); err != nil {
		t.Fatal(err)
	}
	if err := st.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err == nil {
		t.Fatal("finishing feed catchup accepted an execution without fresh evidence")
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
}

func TestRebuiltEngineBlockSurvivesEventPollingAndRejectsGrantRenewal(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := activeAdoptionFixture(t)
	original, err := st.AuthorizeEgressRequest(ctx, queryRequest(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BlockEngineBackend(ctx, b.StoreID, b.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, b.Owner, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("rebuilt backend retained request authority: %v", err)
	}
	var state string
	if err := st.db.QueryRow(`SELECT state FROM egress_grants WHERE grant_id=?`, original.GrantID).Scan(&state); err != nil || state != "revoked" {
		t.Fatalf("backend block did not revoke old grant: %s %v", state, err)
	}
	a, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: b.Owner, ProcessHandle: b.ProcessHandle, Epoch: b.Epoch, EnvironmentFence: b.EnvironmentFence, ReconciledEventSequence: &a.EventSequence}); err == nil {
		t.Fatal("handshake cleared durable engine identity block")
	}
	environment, err := st.GetEnvironmentBinding(ctx, b.EnvironmentID)
	if err != nil || environment.StoreID != b.StoreID || environment.Epoch != b.Epoch || environment.ObservedState != "unknown" {
		t.Fatalf("rebuilt identity reinterpreted old binding: %+v %v", environment, err)
	}
}

func TestLateEngineRepliesCannotMutateAfterProductOwnerChanges(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := activeAdoptionFixture(t)
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='replacement'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, b.Owner, 0, []sandbox.Event{{ID: "late", Sequence: 1, SandboxID: b.SandboxID, Kind: "created"}}); err == nil {
		t.Fatal("old owner committed late event page")
	}
	if err := st.BeginEngineResync(ctx, b.StoreID, b.Owner, 0, 4, 7); err == nil {
		t.Fatal("old owner began event resync")
	}
	if err := st.BlockEngineBackend(ctx, b.StoreID, b.Owner); err == nil {
		t.Fatal("old owner blocked backend")
	}
	if err := st.CommitEnvironmentInspection(ctx, execution.EnvironmentInspection{EnvironmentID: b.EnvironmentID, Owner: b.Owner, Fence: b.EnvironmentFence, EventSequence: -1, Observed: sandbox.Observation{Reference: sandbox.Reference{StoreID: b.StoreID, SandboxID: b.SandboxID, Epoch: b.Epoch, MutationRevision: "engine-revision"}, State: "running", PlacementState: "active"}}); err == nil {
		t.Fatal("old owner committed late inspection")
	}
	current := GenerationLeaseOwner("replacement")
	if err := st.BeginEngineResync(ctx, b.StoreID, current, 0, 4, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, current, 4, []sandbox.Event{{ID: "current", Sequence: 7, SandboxID: b.SandboxID, Kind: "created"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEngineResync(ctx, b.StoreID, b.Owner, 7); err == nil {
		t.Fatal("old owner finished newer owner's resync")
	}
	if cursor, err := st.EngineCursor(ctx, b.StoreID); err != nil || !cursor.Resync || cursor.After != 7 {
		t.Fatalf("old owner modified cursor: %+v %v", cursor, err)
	}
}

func TestPassiveInspectionWorkExcludesLiveProtocolBindings(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := activeAdoptionFixture(t)
	invalidateExecution(t, st, b)
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileEnvironment, b.Owner, "", 16); err != nil || len(work) != 0 {
		t.Fatalf("passive loop selected active protocol binding: %+v %v", work, err)
	}
	op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "pause", EnvironmentID: b.EnvironmentID, Kind: "pause", IdempotencyKey: "pause", ExpectedFence: b.EnvironmentFence})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, SandboxID: b.SandboxID, Epoch: b.Epoch, MutationRevision: "paused"}); err != nil {
		t.Fatal(err)
	}
	// The lifecycle service retires protocol ownership after verified pause.
	if _, err := st.db.Exec(`UPDATE execution_bindings SET state='retired' WHERE generation_id=?`, b.GenerationID); err != nil {
		t.Fatal(err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileEnvironment, b.Owner, "", 16); err != nil || len(work) != 1 || work[0].Key != b.EnvironmentID {
		t.Fatalf("paused invalidation not selected for current inspection: %+v %v", work, err)
	}
}
