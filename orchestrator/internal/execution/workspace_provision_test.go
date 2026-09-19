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
			binding, err := controller.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
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
	if err := st.CreateSession(ctx, store.Session{ID: "second", ExecutionBackend: "khora", UserID: "synthetic", Agent: "sh", Mode: "shell", Status: "created", Workspace: "opaque", RestoreID: "unused", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
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
