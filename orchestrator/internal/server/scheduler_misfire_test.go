package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/store"
)

// A schedule idle across downtime must not replay the windows it missed. This is
// the regression for the storm: the old cursor advanced one window per tick, so a
// day of downtime owed 1447 cold sandboxes and real model calls.
func TestPlanScheduleTickSkipsStaleWindowsInsteadOfReplaying(t *testing.T) {
	spec, err := parseScheduleSpec("*/1 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	// The real incident: next_run_at stuck at 2026-09-11T05:40Z, restart a day later.
	missed := time.Date(2026, 9, 11, 5, 40, 0, 0, time.UTC)
	now := time.Date(2026, 9, 12, 5, 47, 30, 0, time.UTC)
	sc := store.AgentSchedule{CronExpr: "*/1 * * * *", Timezone: "UTC", NextRunAt: &missed}

	plan := planScheduleTick(spec, sc, now, 90*time.Second)

	if plan.Fire {
		t.Fatalf("a window a day stale must not fire, got %+v", plan)
	}
	if !plan.Next.After(now) {
		t.Fatalf("next_run_at %s must be strictly after now %s, else the schedule is immediately due again and the walk continues", plan.Next, now)
	}
	if want := time.Date(2026, 9, 12, 5, 48, 0, 0, time.UTC); !plan.Next.Equal(want) {
		t.Fatalf("next = %s, want the first trigger after now (%s)", plan.Next, want)
	}
	// 05:41Z on 09-11 through 05:47Z on 09-12 inclusive: every window the old
	// cursor would have replayed as a cold sandbox plus a real model call.
	if plan.SkippedWindows != 1448 {
		t.Fatalf("skipped windows = %d, want 1448 (the backlog the old code would have run)", plan.SkippedWindows)
	}
}

// The cursor must always land in the future. If it did not, the next tick would
// find the schedule due again and the one-window-per-tick walk would resume.
func TestPlanScheduleTickAlwaysAdvancesPastNow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expr    string
		lateBy  time.Duration
		grace   time.Duration
		wantFir bool
	}{
		{"stale beyond grace", "*/1 * * * *", 24 * time.Hour, 90 * time.Second, false},
		{"late within grace", "*/1 * * * *", 30 * time.Second, 90 * time.Second, true},
		{"exactly at grace", "*/1 * * * *", 90 * time.Second, 90 * time.Second, true},
		{"just past grace", "*/1 * * * *", 91 * time.Second, 90 * time.Second, false},
		{"hourly stale", "0 * * * *", 72 * time.Hour, 90 * time.Second, false},
		{"daily stale", "0 8 * * *", 30 * 24 * time.Hour, 90 * time.Second, false},
		{"zero grace drops any lateness", "*/1 * * * *", 2 * time.Second, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := parseScheduleSpec(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
			missed := now.Add(-tc.lateBy)
			sc := store.AgentSchedule{CronExpr: tc.expr, Timezone: "UTC", NextRunAt: &missed}
			plan := planScheduleTick(spec, sc, now, tc.grace)
			if plan.Fire != tc.wantFir {
				t.Fatalf("fire = %v, want %v (plan %+v)", plan.Fire, tc.wantFir, plan)
			}
			if !plan.Next.After(now) {
				t.Fatalf("next %s not after now %s: schedule would be immediately due again", plan.Next, now)
			}
		})
	}
}

// Within grace the run still happens, so a restart that straddles a minute
// boundary does not silently drop that window.
func TestPlanScheduleTickFiresWindowWithinGrace(t *testing.T) {
	spec, err := parseScheduleSpec("*/1 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 20, 0, time.UTC)
	missed := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	sc := store.AgentSchedule{CronExpr: "*/1 * * * *", Timezone: "UTC", NextRunAt: &missed}

	plan := planScheduleTick(spec, sc, now, 90*time.Second)
	if !plan.Fire || !plan.ScheduledFor.Equal(missed) {
		t.Fatalf("plan = %+v, want fire for the missed window %s", plan, missed)
	}
	if plan.SkippedWindows != 0 {
		t.Fatalf("skipped = %d, want 0", plan.SkippedWindows)
	}
	if !plan.Next.After(now) {
		t.Fatalf("next %s must be after now %s", plan.Next, now)
	}
}

// Startup must re-anchor a stale cursor in one write rather than leaving the poll
// loop to crawl forward a window at a time.
func TestRecoverStaleScheduleCursorsReanchorsInOnePass(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	stale := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Minute)
	now := time.Now().UTC()
	sc := store.AgentSchedule{ID: "sched_stale", UserID: "alice", Name: "per-minute", Prompt: "tell a story", CronExpr: "*/1 * * * *", Timezone: "UTC", Enabled: true, Mode: "agent", TimeoutSeconds: 60, NextRunAt: &stale, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st}
	srv.cfg.Harness.Scheduler.Enabled = true
	srv.cfg.Harness.Scheduler.MisfireGrace = config.Duration{Duration: 90 * time.Second}

	srv.recoverStaleScheduleCursors(ctx)

	got, err := st.GetSchedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(now) {
		t.Fatalf("next_run_at = %v, want a time after %s", got.NextRunAt, now)
	}
	// No run rows: recording a skip per missed minute would just relocate the
	// backlog from the queue into the table.
	runs, err := st.ListScheduleRuns(ctx, sc.ID, "alice", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("recovery created %d run rows, want 0", len(runs))
	}
}
