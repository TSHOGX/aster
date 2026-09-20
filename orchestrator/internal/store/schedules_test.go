package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduleCRUDAndAtomicClaim(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	sc := AgentSchedule{ID: "sched_test", UserID: "alice", Name: "nightly", Prompt: "hello", CronExpr: "*/5 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now, NextRunAt: &now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSchedule(ctx, sc.ID)
	t.Logf("got %+v", got)
	if got.ResultMode != "new_session" || got.Archived || got.TargetSessionID != "" {
		t.Fatalf("default routing fields = mode=%q archived=%v target=%q", got.ResultMode, got.Archived, got.TargetSessionID)
	}
	run := ScheduleRun{ID: "run_test", ScheduleID: sc.ID, UserID: sc.UserID, ScheduledAt: now, Status: "queued", CreatedAt: now}
	if err := st.ClaimScheduleDue(ctx, sc.ID, got.Version, now.Add(time.Minute), now, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetScheduleRun(ctx, run.ID, sc.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimScheduleDue(ctx, sc.ID, got.Version, now.Add(2*time.Minute), now, ScheduleRun{ID: "run_dup", ScheduleID: sc.ID, UserID: sc.UserID, ScheduledAt: now}); err == nil {
		t.Fatal("expected optimistic claim conflict")
	}
}

func TestScheduleRoutingAndArchiveFields(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	sc := AgentSchedule{ID: "sched_route", UserID: "alice", Name: "contextual", Prompt: "run", CronExpr: "*/5 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", ResultMode: "existing_session", TargetSessionID: "sess_target", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now, NextRunAt: &now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSchedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultMode != "existing_session" || got.TargetSessionID != "sess_target" {
		t.Fatalf("routing fields = mode=%q target=%q", got.ResultMode, got.TargetSessionID)
	}
	if err := st.CreateScheduleRun(ctx, ScheduleRun{ID: "run_archived", ScheduleID: sc.ID, UserID: sc.UserID, ScheduledAt: now, Status: "queued", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	got.Archived = true
	got.Enabled = false
	got.CronExpr = ""
	got.NextRunAt = nil
	if err := st.UpdateSchedule(ctx, got, got.Version); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetSchedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Archived || updated.Enabled || updated.CronExpr != "" || updated.NextRunAt != nil {
		t.Fatalf("updated schedule = %+v", updated)
	}
	if due, err := st.ListDueSchedules(ctx, now.Add(time.Minute), 10); err != nil {
		t.Fatal(err)
	} else if len(due) != 0 {
		t.Fatalf("archived schedule returned as due: %+v", due)
	}
	if queued, err := st.ListQueuedScheduleRuns(ctx, now.Add(time.Minute), 10); err != nil {
		t.Fatal(err)
	} else if len(queued) != 0 {
		t.Fatalf("archived schedule run returned as queued: %+v", queued)
	}
}

func TestListRunningScheduleRunsSupportsRecovery(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	sc := AgentSchedule{ID: "sched_recovery", UserID: "alice", Name: "recovery", Prompt: "run", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, Session{ID: "sess_recovery", UserID: "alice", Status: "created", Agent: "pi", Mode: "agent", Workspace: filepath.Join(t.TempDir(), "workspace"), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	enqueued, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: "sess_recovery", Content: "run", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	turnID := enqueued.TurnID
	started := now.Add(-time.Second)
	if err := st.CreateScheduleRun(ctx, ScheduleRun{ID: "run_recovery", ScheduleID: sc.ID, UserID: sc.UserID, SessionID: "sess_recovery", TurnID: &turnID, ScheduledAt: now.Add(-2 * time.Second), StartedAt: &started, Status: "running", CreatedAt: now.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListRunningScheduleRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "run_recovery" || got[0].SessionID != "sess_recovery" || got[0].TurnID == nil || *got[0].TurnID != turnID {
		t.Fatalf("running recovery rows = %+v", got)
	}
}

// The scheduler writes 'skipped_busy' when a run finds its target session busy.
// The CHECK constraint once omitted the value, so the write failed, the caller
// discarded the error, and the run stayed 'queued' — re-dispatched every poll,
// cold-starting a sandbox and spending real tokens each time.
func TestFinishScheduleRunAcceptsSkippedBusy(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)

	sc := AgentSchedule{ID: "sched_busy", UserID: "alice", Name: "n", Prompt: "p", CronExpr: "*/1 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	run := ScheduleRun{ID: "srun_busy", ScheduleID: sc.ID, UserID: "alice", ScheduledAt: now, Status: "queued", CreatedAt: now}
	if err := st.CreateScheduleRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScheduleRun(ctx, run.ID, "skipped_busy", "session_busy", "busy", "busy", now); err != nil {
		t.Fatalf("FinishScheduleRun(skipped_busy) = %v, want nil", err)
	}
	got, err := st.GetScheduleRun(ctx, run.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "skipped_busy" {
		t.Fatalf("status = %q, want skipped_busy to persist", got.Status)
	}
	// The run must leave the dispatch set, or it loops forever.
	queued, err := st.ListQueuedScheduleRuns(ctx, now.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("dispatchable runs = %d, want 0 (a skipped run must not be re-dispatched)", len(queued))
	}
}

// A terminal transition on a run that is no longer open must be reported, not
// silently treated as success: that difference is how a worker learns its run was
// canceled underneath it.
func TestFinishScheduleRunReportsNotOpen(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)

	sc := AgentSchedule{ID: "sched_notopen", UserID: "alice", Name: "n", Prompt: "p", Enabled: true, Mode: "agent", TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	run := ScheduleRun{ID: "srun_notopen", ScheduleID: sc.ID, UserID: "alice", ScheduledAt: now, Status: "queued", CreatedAt: now}
	if err := st.CreateScheduleRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScheduleRun(ctx, run.ID, "canceled", "user_canceled", "", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScheduleRun(ctx, run.ID, "succeeded", "", "", "", now); !errors.Is(err, ErrScheduleRunNotOpen) {
		t.Fatalf("second finish = %v, want ErrScheduleRunNotOpen", err)
	}
}

// A database created before 'skipped_busy' existed must be migrated by widening
// the CHECK, which needs a table rebuild: a trigger can only tighten a constraint,
// never loosen one, so the users.role precedent does not transfer. Rows, indexes
// and foreign keys must survive.
