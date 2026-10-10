package product

import (
	"harness-platform/orchestrator/internal/sqlitex"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMinuteIntervalCadenceSurvivesEditsDelayAndRestart(t *testing.T) {
	s, scope, _ := fixture(t)
	start := time.Date(2026, 9, 24, 8, 7, 12, 345000000, time.UTC)
	now := start
	s.now = func() time.Time { return now }
	input := Schedule{TriggerKind: "interval", ID: "interval", Title: "Interval", Prompt: "work", Cron: "@every 90m", Timezone: "Asia/Shanghai", State: "active", NextAt: "2099-01-01T00:00:00Z"}
	item, err := s.CreateSchedule(ctx, scope, input)
	must(t, err)
	first := start.Add(90 * time.Minute)
	if item.NextAt != sqlitex.FormatTime(first) {
		t.Fatal("creation accepted client timing", item)
	}
	now = start.Add(20 * time.Minute)
	replay, err := s.CreateSchedule(ctx, scope, input)
	must(t, err)
	if replay.NextAt != item.NextAt {
		t.Fatal("creation retry restarted interval")
	}
	item.Title = "Renamed"
	item.NextAt = "2099-01-01T00:00:00Z"
	must(t, s.UpdateSchedule(ctx, scope, item, false))
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != sqlitex.FormatTime(first) {
		t.Fatal("content edit postponed interval", item)
	}
	now = first.Add(37 * time.Second)
	n, err := s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != sqlitex.FormatTime(first.Add(90*time.Minute)) {
		t.Fatal("tick drifted", item)
	}
	preview := s.PreviewSchedule(item.Cron, item.Timezone, item.NextAt)
	if !preview.Valid || preview.Occurrences[0] != item.NextAt {
		t.Fatal("preview lost phase", preview)
	}
	// Resolve the prior occurrence before the next automatic admission.
	_, err = s.db.Exec(`UPDATE turns SET disposition='handled' WHERE id IN (SELECT turn_id FROM schedule_runs WHERE schedule_id=?)`, item.ID)
	must(t, err)
	// A new Store instance has no in-memory anchor; next_at carries the phase.
	var seq int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	must(t, s.Close())
	s, err = openFixtureStore(ctx, path)
	must(t, err)
	defer s.Close()
	s.now = func() time.Time { return now }
	now = first.Add(5*90*time.Minute + 37*time.Second)
	n, err = s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 1 {
		t.Fatal("missed intervals were not coalesced", n)
	}
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != sqlitex.FormatTime(first.Add(6*90*time.Minute)) {
		t.Fatal("restart drifted", item)
	}
	runs, err := s.ScheduleRuns(ctx, scope, item.ID, "", 50)
	must(t, err)
	if len(runs) != 2 {
		t.Fatal("missed ticks duplicated work", runs)
	}
	next := item.NextAt
	_, err = s.RunSchedule(ctx, scope, item.ID, "manual")
	must(t, err)
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != next {
		t.Fatal("manual run changed cadence")
	}
	item.Timezone = "America/New_York"
	must(t, s.UpdateSchedule(ctx, scope, item, false))
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != next {
		t.Fatal("display timezone reset interval")
	}
	item.State = "paused"
	must(t, s.UpdateSchedule(ctx, scope, item, false))
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != "" {
		t.Fatal(item)
	}
	now = now.Add(4 * time.Hour)
	item.State = "active"
	must(t, s.UpdateSchedule(ctx, scope, item, false))
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != sqlitex.FormatTime(now.Add(90*time.Minute)) {
		t.Fatal("enable did not restart timer", item)
	}
	now = now.Add(time.Minute)
	item.Cron = "@every 7m"
	must(t, s.UpdateSchedule(ctx, scope, item, false))
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != sqlitex.FormatTime(now.Add(7*time.Minute)) {
		t.Fatal("interval edit did not restart timer", item)
	}
}

func TestMinuteIntervalValidationAndElapsedPreview(t *testing.T) {
	now := time.Date(2026, 3, 8, 6, 30, 0, 123, time.UTC)
	s := &Store{now: func() time.Time { return now }}
	for _, rule := range []string{"@every 0m", "@every -1m", "@every 1.5m", "@every 10081m", "@every 1h", "@every 60s", "@every +1m", "@every 01m", "@every 1m ", "@every 999999999999999999999m"} {
		p := s.PreviewSchedule(rule, "UTC", "")
		if p.Valid || p.Errors["interval"] == "" || len(p.Occurrences) != 0 {
			t.Fatal(rule, p)
		}
	}
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		p := s.PreviewSchedule("@every 90m", zone, "")
		expected := []string{sqlitex.FormatTime(now.Add(90 * time.Minute)), sqlitex.FormatTime(now.Add(180 * time.Minute)), sqlitex.FormatTime(now.Add(270 * time.Minute))}
		if !p.Valid || !reflect.DeepEqual(p.Occurrences, expected) {
			t.Fatal("DST/zone changed elapsed interval", zone, p)
		}
	}
	for _, rule := range []string{"@every 1m", "@every 10080m"} {
		if !s.PreviewSchedule(rule, "UTC", "").Valid {
			t.Fatal(rule)
		}
	}
	if s.PreviewSchedule("@every 15m", "UTC", "bad-anchor").Errors["next_at"] == "" {
		t.Fatal("invalid anchor accepted")
	}
	anchor := now.Add(-31 * time.Minute)
	p := s.PreviewSchedule("@every 15m", "UTC", sqlitex.FormatTime(anchor))
	if p.Occurrences[0] != sqlitex.FormatTime(now.Add(14*time.Minute)) {
		t.Fatal("past anchor lost cadence", p)
	}
}

func TestMinuteIntervalConcurrentTicksAdmitOnce(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	_, err := s.CreateSchedule(ctx, scope, Schedule{TriggerKind: "interval", ID: "interval", Title: "Interval", Prompt: "work", Cron: "@every 1m", Timezone: "UTC", State: "active"})
	must(t, err)
	now = now.Add(3*time.Minute + time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.TickSchedules(ctx, 10); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	runs, err := s.ScheduleRuns(ctx, scope, "interval", "", 50)
	must(t, err)
	if len(runs) != 1 {
		t.Fatal(runs)
	}
	page, err := s.ScheduleOverviewPage(ctx, scope.Actor, ScheduleFilter{})
	must(t, err)
	if len(page.Schedules) != 1 || page.Schedules[0].Cron != "@every 1m" || page.Schedules[0].NextAt == "" {
		t.Fatal(page)
	}
}

func TestScheduleContentEditPreservesOverdueCron(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	item, err := s.CreateSchedule(ctx, scope, Schedule{TriggerKind: "cron", ID: "daily", Title: "Daily", Prompt: "work", Cron: "0 9 * * *", Timezone: "UTC", State: "active"})
	must(t, err)
	due := item.NextAt
	now = now.Add(2 * time.Hour)
	item.Prompt = "edited work"
	must(t, s.UpdateSchedule(ctx, scope, item, false))
	item, err = s.Schedule(ctx, scope, item.ID)
	must(t, err)
	if item.NextAt != due {
		t.Fatal("edit dropped an overdue occurrence", item)
	}
}
