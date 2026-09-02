package store

import (
	"context"
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
