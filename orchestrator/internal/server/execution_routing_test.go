package server

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"log/slog"
	"testing"
	"time"
)

type routedController struct {
	calls int
	fail  bool
}

func (c *routedController) ReleaseIdle(context.Context, string, string, string) error { return nil }

func (c *routedController) Ensure(_ context.Context, p execution.Session, owner string) (execution.Binding, error) {
	c.calls++
	if c.fail {
		return execution.Binding{}, errors.New("engine unavailable")
	}
	return execution.Binding{SessionID: p.ID, GenerationID: "opaque-generation", Owner: owner}, nil
}

func TestPersistedKhoraRoutingNeverFallsBackToEmbeddedRuntime(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, directory)
	session := store.Session{ID: "khora-session", ExecutionBackend: "khora", UserID: "lab", Agent: "sh", Mode: "shell", Status: "created", Workspace: "workspace:khora-session", RestoreID: "unused", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st, cfg: testServerConfig(directory), hub: events.NewHub(), log: slog.Default(), ownerUUID: owner.UUID}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	if _, err := srv.ensureActiveGeneration(ctx, session, leaseOwner); err == nil {
		t.Fatal("missing Khora controller fell back to embedded runtime")
	}
	controller := &routedController{fail: true}
	if err := srv.SetExecutionController(controller, "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureActiveGeneration(ctx, session, leaseOwner); err == nil {
		t.Fatal("failed Khora controller fell back to embedded runtime")
	}
	controller.fail = false
	ensured, err := srv.ensureActiveGeneration(ctx, session, leaseOwner)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.Backend != "khora" || ensured.Allocation.GenerationID != "opaque-generation" {
		t.Fatalf("lost opaque execution: %+v", ensured)
	}
	if err := srv.startEnsuredGeneration(ctx, session, ensured, startFailureInputAcceptable); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"network_profiles", "runtime_resource_instances", "session_driver_homes", "sandbox_environments"} {
		var count int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("routing created legacy data: %s %d %v", table, count, err)
		}
	}
	if controller.calls != 2 {
		t.Fatalf("creation policy changed persisted session routing: %d", controller.calls)
	}
}

func TestScheduledSessionsUsePinnedBackendForNewAndExistingTargets(t *testing.T) {
	for _, mode := range []string{"new_session", "existing_session"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			st, owner := openServerOwnedStore(t, ctx, directory)
			now := time.Now().UTC()
			controller := &routedController{fail: true}
			srv := &Server{store: st, cfg: testServerConfig(directory), hub: events.NewHub(), log: slog.Default(), ownerUUID: owner.UUID}
			creation := "khora"
			target := ""
			if mode == "existing_session" {
				creation = "legacy"
				target = "existing-khora"
				if err := st.CreateSession(ctx, store.Session{ID: target, ExecutionBackend: "khora", UserID: "lab", Agent: "pi", Mode: "agent", Status: "created", Workspace: "opaque", RestoreID: "unused", CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			if err := srv.SetExecutionController(controller, creation); err != nil {
				t.Fatal(err)
			}
			schedule := store.AgentSchedule{ID: "schedule", UserID: "lab", Name: "scheduled", Prompt: "run", Enabled: true, Mode: "agent", ResultMode: mode, TargetSessionID: target, TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
			if err := st.CreateSchedule(ctx, schedule); err != nil {
				t.Fatal(err)
			}
			run := store.ScheduleRun{ID: "run", ScheduleID: schedule.ID, UserID: "lab", ScheduledAt: now, CreatedAt: now, Status: "queued"}
			if err := st.CreateScheduleRun(ctx, run); err != nil {
				t.Fatal(err)
			}
			srv.executeScheduleRun(ctx, run)
			if controller.calls != 1 {
				t.Fatalf("scheduler bypassed persisted Khora backend: %d", controller.calls)
			}
			sessions, err := st.ListSessions(ctx)
			if err != nil || len(sessions) != 1 {
				t.Fatalf("scheduled session creation: %+v %v", sessions, err)
			}
			if selected, err := st.SessionExecutionBackend(ctx, sessions[0].ID); err != nil || selected != "khora" {
				t.Fatalf("scheduled backend: %s %v", selected, err)
			}
			if sessions[0].AgentHomePath != "" || sessions[0].AutoCheckpointEnabled {
				t.Fatal("scheduled Khora session created legacy home/checkpoint policy")
			}
		})
	}
}
