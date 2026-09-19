package store

import (
	"context"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func TestEnvironmentCapacitySurvivesExecutionFailureUntilVerifiedPause(t *testing.T) {
	ctx := context.Background()
	st, _, binding, observed := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailGenerationWithEvent(ctx, FailGenerationParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, ErrorClass: "transport_lost", Reason: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 1)
	if err := st.UpdateSessionStatus(ctx, binding.SessionID, "failed", nil); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 1)
	op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "pause", EnvironmentID: binding.EnvironmentID, Kind: "pause", IdempotencyKey: "pause", ExpectedFence: binding.EnvironmentFence})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationUncertain, Error: "stop reply lost"}); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 1)
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, SandboxID: binding.SandboxID, Epoch: binding.Epoch, MutationRevision: "paused"}); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 0)
}

func TestEnvironmentCapacityReservesCreateAndResumeBeforeRemoteReplies(t *testing.T) {
	ctx := context.Background()
	st, environment := operationFixture(t)
	assertCapacity(t, st, 0)
	for i, kind := range []string{"create", "pause", "resume"} {
		op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: kind, EnvironmentID: environment.ID, Kind: kind, IdempotencyKey: kind, ExpectedFence: int64(i)})
		if err != nil {
			t.Fatal(err)
		}
		assertCapacity(t, st, 1)
		if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationUncertain, Error: "lost reply"}); err != nil {
			t.Fatal(err)
		}
		assertCapacity(t, st, 1)
		if kind == "resume" {
			if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationFailed, Error: "verified capacity rejection"}); err != nil {
				t.Fatal(err)
			}
			assertCapacity(t, st, 0)
		} else {
			if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, SandboxID: "sandbox", Epoch: 1, MutationRevision: kind}); err != nil {
				t.Fatal(err)
			}
			if kind == "pause" {
				assertCapacity(t, st, 0)
			} else {
				assertCapacity(t, st, 1)
			}
		}
	}
}

func TestCapacityInspectionCannotPromoteExecutionOrOvertakeNewEvent(t *testing.T) {
	ctx := context.Background()
	st, _, binding, observed := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, 0, []sandbox.Event{{ID: "created", Sequence: 1, SandboxID: binding.SandboxID, Kind: "created"}}); err != nil {
		t.Fatal(err)
	}
	inspection := execution.EnvironmentInspection{EnvironmentID: binding.EnvironmentID, Fence: binding.EnvironmentFence, EventSequence: 1, Observed: sandbox.Observation{Reference: sandbox.Reference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch, MutationRevision: "engine-revision"}, State: "running", PlacementState: "active"}}
	if err := st.CommitEnvironmentInspection(ctx, inspection); err != nil {
		t.Fatal(err)
	}
	if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, binding.Owner, time.Now()); err == nil {
		t.Fatal("control-plane inspect restored agent authority")
	}
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, 1, []sandbox.Event{{ID: "paused", Sequence: 2, SandboxID: binding.SandboxID, Kind: "paused"}}); err != nil {
		t.Fatal(err)
	}
	inspection.Observed.State = "paused"
	inspection.Observed.PlacementState = "lost"
	if err := st.CommitEnvironmentInspection(ctx, inspection); err == nil {
		t.Fatal("old inspect released capacity after newer event")
	}
	assertCapacity(t, st, 1)
}

func TestUnexpectedEpochInspectionKeepsRecordedIdentityAndCapacity(t *testing.T) {
	ctx := context.Background()
	st, _, binding, _ := boundExecutionFixture(t)
	inspection := execution.EnvironmentInspection{EnvironmentID: binding.EnvironmentID, Fence: binding.EnvironmentFence, EventSequence: -1, Observed: sandbox.Observation{Reference: sandbox.Reference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch + 1, MutationRevision: "replacement"}, State: "paused", PlacementState: "lost"}}
	if err := st.CommitEnvironmentInspection(ctx, inspection); err == nil {
		t.Fatal("unexpected epoch silently adopted")
	}
	environment, err := st.GetEnvironmentBinding(ctx, binding.EnvironmentID)
	if err != nil || environment.Epoch != binding.Epoch || environment.ObservedState != "unknown" {
		t.Fatalf("unexpected placement rewrote identity: %+v %v", environment, err)
	}
	assertCapacity(t, st, 1)
}

func TestPausedInspectionSettlesHistoricalCapacityInvalidation(t *testing.T) {
	ctx := context.Background()
	st, _, binding, _ := boundExecutionFixture(t)
	op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "pause", EnvironmentID: binding.EnvironmentID, Kind: "pause", IdempotencyKey: "pause", ExpectedFence: binding.EnvironmentFence})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, SandboxID: binding.SandboxID, Epoch: binding.Epoch, MutationRevision: "paused"}); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 0)
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, 0, []sandbox.Event{{ID: "old-created", Sequence: 1, SandboxID: binding.SandboxID, Kind: "created"}}); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 1)
	if err := st.CommitEnvironmentInspection(ctx, execution.EnvironmentInspection{EnvironmentID: binding.EnvironmentID, Fence: op.Fence, EventSequence: 1, Observed: sandbox.Observation{Reference: sandbox.Reference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch, MutationRevision: "paused"}, State: "paused", PlacementState: "lost"}}); err != nil {
		t.Fatal(err)
	}
	assertCapacity(t, st, 0)
}

func assertCapacity(t *testing.T, st *Store, want int) {
	t.Helper()
	got, err := st.CountSandboxOccupyingSessions(context.Background())
	if err != nil || got != want {
		t.Fatalf("capacity=%d want=%d: %v", got, want, err)
	}
}
