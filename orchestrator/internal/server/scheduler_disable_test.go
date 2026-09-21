package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/store"
)

func newDisableTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// A run already handed to a worker must not execute once its schedule is off.
// Previously the worker trusted the row it received from the queue, whose enabled
// flag was read when it was selected, so disabling could not reach it and only a
// restart (discarding the channel) stopped the work.
func TestQueuedRunDoesNotStartAfterScheduleDisabled(t *testing.T) {
	ctx := context.Background()
	st := newDisableTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	sc := store.AgentSchedule{ID: "sched_disable", UserID: "alice", Name: "story", Prompt: "tell a story", CronExpr: "*/1 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", TimeoutSeconds: 60, NextRunAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	run := store.ScheduleRun{ID: "srun_disable", ScheduleID: sc.ID, UserID: "alice", ScheduledAt: now, Status: "queued", CreatedAt: now}
	if err := st.CreateScheduleRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// The run is in flight (its id is on the queue) when the user disables.
	current, err := st.GetSchedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Enabled = false
	if err := st.UpdateSchedule(ctx, current, current.Version); err != nil {
		t.Fatal(err)
	}

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, schedulerRunsActive: map[string]struct{}{run.ID: {}}}
	srv.startScheduleRunFromQueue(ctx, run.ID)

	got, err := st.GetScheduleRun(ctx, run.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "canceled" {
		t.Fatalf("run status = %q, want canceled (a disabled schedule must not start queued work)", got.Status)
	}
	if got.ErrorClass != "schedule_disabled" {
		t.Fatalf("error_class = %q, want schedule_disabled so the reason is legible", got.ErrorClass)
	}
	// No session was created and no prompt was enqueued: the gate ran before any
	// of that, which is the point of claiming ahead of the work.
	if got.SessionID != "" || got.TurnID != nil {
		t.Fatalf("run touched a session/turn: session=%q turn=%v", got.SessionID, got.TurnID)
	}
}

// A run canceled while it waited must not start either, even with the schedule
// still enabled.
func TestCanceledRunIsNotStarted(t *testing.T) {
	ctx := context.Background()
	st := newDisableTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	sc := store.AgentSchedule{ID: "sched_cancel", UserID: "alice", Name: "story", Prompt: "p", CronExpr: "*/1 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	run := store.ScheduleRun{ID: "srun_cancel", ScheduleID: sc.ID, UserID: "alice", ScheduledAt: now, Status: "queued", CreatedAt: now}
	if err := st.CreateScheduleRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScheduleRun(ctx, run.ID, "canceled", "user_canceled", "canceled", "", now); err != nil {
		t.Fatal(err)
	}

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, schedulerRunsActive: map[string]struct{}{run.ID: {}}}
	srv.startScheduleRunFromQueue(ctx, run.ID)

	got, _ := st.GetScheduleRun(ctx, run.ID, "alice")
	if got.Status != "canceled" || got.SessionID != "" {
		t.Fatalf("run = %+v, want it left canceled and untouched", got)
	}
}

// The claim must reject a run that is no longer queued, so a cancel that lands
// between dispatch and execution actually prevents the work.
func TestClaimScheduleRunStartRefusesCanceledRun(t *testing.T) {
	ctx := context.Background()
	st := newDisableTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	sc := store.AgentSchedule{ID: "sched_claim", UserID: "alice", Name: "n", Prompt: "p", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	run := store.ScheduleRun{ID: "srun_claim", ScheduleID: sc.ID, UserID: "alice", ScheduledAt: now, Status: "queued", CreatedAt: now}
	if err := st.CreateScheduleRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimScheduleRunStart(ctx, run.ID, now); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// A second claim must fail: the run is no longer queued.
	if err := st.ClaimScheduleRunStart(ctx, run.ID, now); !errors.Is(err, store.ErrScheduleRunNotOpen) {
		t.Fatalf("second claim err = %v, want ErrScheduleRunNotOpen", err)
	}
}

// Disabling through the API retires the queued backlog, so a later re-enable does
// not release a burst of stale runs.
func TestDisablingScheduleCancelsQueuedBacklog(t *testing.T) {
	ctx := context.Background()
	st := newDisableTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	sc := store.AgentSchedule{ID: "sched_backlog", UserID: "alice", Name: "story", Prompt: "p", CronExpr: "*/1 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", TimeoutSeconds: 60, NextRunAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := st.CreateScheduleRun(ctx, store.ScheduleRun{ID: "srun_b" + string(rune('a'+i)), ScheduleID: sc.ID, UserID: "alice", ScheduledAt: now.Add(time.Duration(i) * time.Minute), Status: "queued", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st}
	req := httptest.NewRequest(http.MethodPatch, "/api/schedules/"+sc.ID, strings.NewReader(`{"enabled":false}`))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, "alice"))
	rec := httptest.NewRecorder()
	srv.scheduleRoute(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated store.AgentSchedule
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("schedule still enabled after PATCH")
	}

	pending, err := st.CountPendingScheduleRuns(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending runs after disable = %d, want 0", pending)
	}
	// And nothing is dispatchable, which is the durable half of the fix.
	queued, err := st.ListQueuedScheduleRuns(ctx, now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("dispatchable runs after disable = %d, want 0", len(queued))
	}
}

// The backlog ceiling applies to manual runs too, which are otherwise unbounded.
func TestManualRunRejectedWhenBacklogAtLimit(t *testing.T) {
	ctx := context.Background()
	st := newDisableTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	sc := store.AgentSchedule{ID: "sched_manual", UserID: "alice", Name: "n", Prompt: "p", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, schedulerWake: make(chan struct{}, 1)}
	srv.cfg.Harness.Scheduler.MaxPendingRunsPerSchedule = 2

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/schedules/"+sc.ID+"/run", nil)
		srv.scheduleRoute(rec, req.WithContext(context.WithValue(req.Context(), userContextKey{}, "alice")))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("manual run %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/"+sc.ID+"/run", nil)
	srv.scheduleRoute(rec, req.WithContext(context.WithValue(req.Context(), userContextKey{}, "alice")))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third manual run status=%d, want 429 (backlog ceiling)", rec.Code)
	}
}
