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
