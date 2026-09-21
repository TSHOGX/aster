package execution_test

import (
	"context"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
	"testing"
	"time"
)

func TestNewWorkspaceProvisioningRetainsVolumeWithoutAgentAndRetriesLostReplies(t *testing.T) {
	for _, lost := range []string{"create", "pause"} {
		t.Run(lost, func(t *testing.T) {
			ctx := context.Background()
			controller, st, backend, launcher, owner := controllerFixture(t)
			backend.loseCreate = lost == "create"
			backend.losePause = lost == "pause"
			if err := controller.EnsureWorkspace(ctx, "session", owner, 2); err == nil {
				t.Fatal("lost provisioning reply ignored")
			}
			if err := controller.EnsureWorkspace(ctx, "session", owner, 2); err != nil {
				t.Fatal(err)
			}
			if err := controller.EnsureWorkspace(ctx, "session", owner, 2); err != nil {
				t.Fatal(err)
			}
			if len(launcher.requests) != 0 || backend.volumeCalls != 2 {
				t.Fatalf("workspace provision launched agent or recreated data: starts=%d volumes=%d", len(launcher.requests), backend.volumeCalls)
			}
			if previous, found, err := st.FindLastExecutionBinding(ctx, "session"); err != nil || found {
				t.Fatalf("workspace provisioning created execution: %+v %t %v", previous, found, err)
			}
			environment, err := st.GetSessionEnvironment(ctx, "session")
			if err != nil || environment.ObservedState != "paused" {
				t.Fatalf("provisioning not retained/paused: %+v %v", environment, err)
			}
			if count, err := st.CountSandboxOccupyingSessions(ctx); err != nil || count != 0 {
				t.Fatalf("provisioning leaked active capacity: %d %v", count, err)
			}
			binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "pi"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			if binding.Epoch != environment.Epoch+1 || binding.SandboxID != environment.SandboxID || backend.volumeCalls != 2 || len(launcher.requests) != 1 {
				t.Fatalf("initial execution discarded workspace: %+v", binding)
			}
		})
	}
}

func TestWorkspaceProvisioningRejectsStaleOwnerBeforeAllocatingStorage(t *testing.T) {
	ctx := context.Background()
	controller, _, backend, _, _ := controllerFixture(t)
	if err := controller.EnsureWorkspace(ctx, "session", "foreign-owner", 2); err == nil {
		t.Fatal("stale owner provisioned workspace")
	}
	if backend.volumeCalls != 0 || len(backend.createKeys) != 0 {
		t.Fatal("ownership checked after side effects")
	}
}

func TestWorkspaceProvisioningInspectsAfterLaterMaintenanceInvalidation(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, launcher, owner := controllerFixture(t)
	if err := controller.EnsureWorkspace(ctx, "session", owner, 2); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetSessionEnvironment(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	// A later completed maintenance activation advanced both the placement and
	// product fence. The initial provision-pause intent must never be replayed.
	backend.current.Epoch++
	backend.current.MutationRevision = "later-paused-revision"
	if _, err := st.DBForTest().Exec(`UPDATE sandbox_environments SET epoch=?,mutation_revision=?,fence=fence+2,observed_state='paused' WHERE environment_id=?`, backend.current.Epoch, backend.current.MutationRevision, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitEngineEvents(ctx, e.StoreID, owner, 0, []sandbox.Event{{ID: "late-maintenance", Sequence: 1, SandboxID: e.SandboxID, Epoch: backend.current.Epoch, Kind: "paused"}}); err != nil {
		t.Fatal(err)
	}
	calls := backend.calls
	if err := controller.EnsureWorkspace(ctx, "session", owner, 2); err != nil {
		t.Fatal(err)
	}
	if backend.calls != calls || len(launcher.requests) != 0 {
		t.Fatal("workspace revalidation mutated lifecycle or started an agent")
	}
	if current, err := st.GetSessionEnvironment(ctx, "session"); err != nil || current.ObservedState != "paused" || current.Epoch != backend.current.Epoch {
		t.Fatalf("workspace inspection failed to settle current state: %+v %v", current, err)
	}
}

func TestWorkspaceProvisioningHonorsActiveSessionLimit(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, owner := controllerFixture(t)
	if err := controller.EnsureWorkspace(ctx, "session", owner, 0); err == nil {
		t.Fatal("unbounded workspace provision accepted")
	}
	if err := controller.EnsureWorkspace(ctx, "session", owner, 1); err != nil {
		t.Fatal(err)
	}
	// Pending resume counts before any remote placement has replied.
	environment, err := st.GetSessionEnvironment(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginInitialExecutionResume(ctx, environment.ID, owner, 1); err != nil {
		t.Fatal(err)
	}
	if count, err := st.CountSandboxOccupyingSessions(ctx); err != nil || count != 1 {
		t.Fatalf("initial resume uncharged: %d %v", count, err)
	}
	if err := st.CreateSession(ctx, store.Session{ID: "second", UserID: "synthetic", Agent: "pi", Mode: "shell", Status: "created", Workspace: "opaque", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnsureWorkspace(ctx, "second", owner, 1); !sandbox.Is(err, sandbox.Capacity) {
		t.Fatalf("second environment crossed reserved capacity: %v", err)
	}
	other, err := st.GetSessionEnvironment(ctx, "second")
	if err != nil || other.SandboxID != "" {
		t.Fatalf("capacity refusal created sandbox: %+v %v", other, err)
	}
	// An immutable retry owns its original reservation despite the full limit.
	if _, err := st.BeginInitialExecutionResume(ctx, environment.ID, owner, 1); sandbox.Is(err, sandbox.Capacity) || err != nil {
		t.Fatalf("resume retry lost capacity ownership: %v", err)
	}
}
