package execution_test

import (
	"context"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
	"testing"
)

type lateStartupEvent struct {
	*store.Store
	once bool
}

func (r *lateStartupEvent) AcceptExecutionBinding(ctx context.Context, p execution.ObserveBindingParams) error {
	if !r.once {
		r.once = true
		b, err := r.GetExecutionBinding(ctx, p.GenerationID)
		if err != nil {
			return err
		}
		if _, err := r.CommitEngineEvents(ctx, b.StoreID, p.Owner, 0, []sandbox.Event{{ID: "late-created", Sequence: 1, SandboxID: b.SandboxID, Epoch: b.Epoch, Kind: "created"}}); err != nil {
			return err
		}
	}
	return r.Store.AcceptExecutionBinding(ctx, p)
}
func TestStartupRevalidatesLateEngineEventUsingOriginalStartIdentity(t *testing.T) {
	controller, st, _, launcher, owner := controllerFixture(t)
	controller.Repository = &lateStartupEvent{Store: st}
	binding, err := controller.Ensure(context.Background(), execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.requests) != 2 || launcher.requests[0].IdempotencyKey != launcher.requests[1].IdempotencyKey || launcher.requests[0].ProcessReference != launcher.requests[1].ProcessReference {
		t.Fatalf("readiness retry changed durable start: %+v", launcher.requests)
	}
	var generations int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM execution_bindings`).Scan(&generations); err != nil || generations != 1 {
		t.Fatalf("readiness retry created a new generation: %d %v", generations, err)
	}
	if sequence, err := st.EnvironmentInvalidationSequence(context.Background(), binding.EnvironmentID); err != nil || sequence != -1 {
		t.Fatalf("fresh readiness did not settle event: %d %v", sequence, err)
	}
}
