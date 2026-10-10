package product

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSchedulesShareForegroundAdmissionAndDeduplicate(t *testing.T) {
	s, a, b := fixture(t)
	clock := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	_, err := s.CreateSession(ctx, a, "existing", "Agent")
	must(t, err)
	_, err = s.CreateSession(ctx, b, "foreign", "Foreign")
	must(t, err)
	input := Schedule{TriggerKind: "cron", ID: "daily", Title: "Daily check", Prompt: "inspect workspace", Cron: "0 8 * * *", Timezone: "Asia/Shanghai", State: "active"}
	schedule, err := s.CreateSchedule(ctx, a, input)
	must(t, err)
	if schedule.NextAt != "2026-09-22T00:00:00.000000000Z" {
		t.Fatal(schedule.NextAt)
	}
	input.ID = "bad"
	input.SessionID = "foreign"
	_, err = s.CreateSchedule(ctx, a, input)
	wantError(t, err, ErrNotFound)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.RunSchedule(ctx, a, "daily", "same-request"); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	runs, err := s.ScheduleRuns(ctx, a, "daily", "", 100)
	must(t, err)
	if len(runs) != 1 || runs[0].ProjectID != a.ProjectID {
		t.Fatal(runs)
	}
	child, err := s.Session(ctx, a, runs[0].SessionID)
	must(t, err)
	if child.ProjectID != a.ProjectID {
		t.Fatal(child)
	}
	_, err = s.AdmitTurn(ctx, a, "existing", "foreground", "foreground work", false)
	must(t, err)
	blockers, err := s.Blockers(ctx, a)
	must(t, err)
	if len(blockers) != 2 {
		t.Fatal(blockers)
	}
	// An unresolved occurrence blocks automatic overlap and retains its due time.
	clock = clock.Add(3 * 24 * time.Hour)
	blocked, err := s.TickSchedules(ctx, 32)
	must(t, err)
	if blocked != 0 {
		t.Fatal("overlap admitted", blocked)
	}
	_, err = s.db.Exec(`UPDATE turns SET disposition='handled' WHERE id=?`, runs[0].TurnID)
	must(t, err)
	// Three missed days coalesce into one run and advance past now.
	clock = clock.Add(time.Hour)
	count, err := s.TickSchedules(ctx, 32)
	must(t, err)
	if count != 1 {
		t.Fatal(count)
	}
	count, err = s.TickSchedules(ctx, 32)
	must(t, err)
	if count != 0 {
		t.Fatal("duplicate cron", count)
	}
	runs, err = s.ScheduleRuns(ctx, a, "daily", "", 100)
	must(t, err)
	if len(runs) != 2 {
		t.Fatal(runs)
	}
	next, err := s.Schedule(ctx, a, "daily")
	must(t, err)
	if next.NextAt < s.timestamp() {
		t.Fatal(next)
	}
	schedule.Prompt = "changed"
	must(t, s.UpdateSchedule(ctx, a, schedule, false))
	// Settling this run cannot release the foreground turn's admission.
	_, err = s.db.Exec(`UPDATE turns SET disposition='handled' WHERE id=?`, runs[0].TurnID)
	must(t, err)
	blockers, err = s.Blockers(ctx, a)
	must(t, err)
	if len(blockers) != 1 {
		t.Fatal(blockers)
	}
}
func TestScheduleDeletionClosesAllAdmission(t *testing.T) {
	s, a, _ := fixture(t)
	_, err := s.CreateSession(ctx, a, "target", "Target")
	must(t, err)
	for i, session := range []string{"target", ""} {
		_, err = s.CreateSchedule(ctx, a, Schedule{TriggerKind: "cron", ID: fmt.Sprint("s", i), SessionID: session, Title: "Check", Prompt: "run", Cron: "* * * * *", Timezone: "UTC", State: "active"})
		must(t, err)
	}
	child, err := s.Session(ctx, a, "target")
	must(t, err)
	must(t, s.DeleteSession(ctx, a, child.ID, child.Revision))
	schedule, err := s.Schedule(ctx, a, "s0")
	must(t, err)
	if schedule.State == "active" || schedule.NextAt != "" {
		t.Fatal(schedule)
	}
	_, err = s.RunSchedule(ctx, a, "s0", "late")
	wantError(t, err, ErrConflict)
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, s.DeleteProject(ctx, a, p.Revision))
	_, err = s.RunSchedule(ctx, a, "s1", "late")
	wantError(t, err, ErrDeleting)
	count, err := s.TickSchedules(ctx, 32)
	must(t, err)
	if count != 0 {
		t.Fatal(count)
	}
}
func TestScheduleManualOnlyAndInvalidCron(t *testing.T) {
	s, a, _ := fixture(t)
	for _, bad := range []Schedule{
		{ID: "bad", Title: "x", Prompt: "x", Cron: "@every 1s", Timezone: "UTC"},
		{ID: "bad", Title: "x", Prompt: "x", Cron: "* * * * *", Timezone: "bogus"},
		{ID: "bad", Title: "x", Prompt: "x", Cron: "0 0 31 2 *", Timezone: "UTC"},
	} {
		_, err := s.CreateSchedule(ctx, a, bad)
		wantError(t, err, ErrInvalid)
	}
	item, err := s.CreateSchedule(ctx, a, Schedule{TriggerKind: "manual", ID: "manual", Title: "Manual", Prompt: "work", Timezone: "UTC", State: "active"})
	must(t, err)
	if item.NextAt != "" {
		t.Fatal(item)
	}
	_, err = s.RunSchedule(context.Background(), a, item.ID, "one")
	must(t, err)
	item.State = "paused"
	must(t, s.UpdateSchedule(ctx, a, item, false))
	_, err = s.RunSchedule(ctx, a, item.ID, "two")
	wantError(t, err, ErrConflict)
	// An exact retry still observes the original run despite later disablement.
	_, err = s.RunSchedule(ctx, a, item.ID, "one")
	must(t, err)
}
