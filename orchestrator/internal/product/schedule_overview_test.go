package product

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScheduleOverviewOwnershipPaginationAndDeletion(t *testing.T) {
	s, a, b := fixture(t)
	_, err := s.CreateProject(ctx, a.Actor, "second", "Second")
	must(t, err)
	for i := 0; i < 105; i++ {
		scope := a
		if i%2 == 1 {
			scope.ProjectID = "second"
		}
		_, err = s.CreateSchedule(ctx, scope, Schedule{ID: fmt.Sprintf("schedule-%03d", i), Title: "Task", Prompt: "work", Timezone: "UTC", Enabled: true})
		must(t, err)
	}
	_, err = s.CreateSchedule(ctx, b, Schedule{ID: "foreign", Title: "Secret", Prompt: "work", Timezone: "UTC", Enabled: true})
	must(t, err)
	first, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Limit: 50})
	must(t, err)
	if len(first.Schedules) != 50 || first.Next == "" {
		t.Fatal(first)
	}
	second, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{After: first.Next, Limit: 50})
	must(t, err)
	if len(second.Schedules) != 50 || second.Next == "" || second.Schedules[0].ID <= first.Schedules[49].ID {
		t.Fatal(second)
	}
	for _, item := range first.Schedules {
		if item.ProjectTitle == "" || item.ProjectID == b.ProjectID {
			t.Fatal(item)
		}
	}
	filtered, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Project: "second", Limit: 50})
	must(t, err)
	if len(filtered.Schedules) != 50 || filtered.Next == "" {
		t.Fatal(len(filtered.Schedules))
	}
	foreign, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Project: b.ProjectID, Limit: 50})
	must(t, err)
	if len(foreign.Schedules) != 0 {
		t.Fatal(foreign)
	}
	p, err := s.Project(ctx, Scope{a.Actor, "second"})
	must(t, err)
	must(t, s.DeleteProject(ctx, Scope{a.Actor, "second"}, p.Revision))
	filtered, err = s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Project: "second", Limit: 50})
	must(t, err)
	if len(filtered.Schedules) != 0 {
		t.Fatal("deleting project leaked", filtered)
	}
}

func TestScheduleOverviewFiltersOrderAndInvalidatedCursor(t *testing.T) {
	s, a, b := fixture(t)
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 0; i < 65; i++ {
		input := Schedule{ID: fmt.Sprintf("task-%03d", i), Title: fmt.Sprintf("ÄBC report %d", i), Prompt: "private", Timezone: "UTC", Enabled: i < 60}
		if i%2 == 0 {
			input.Cron = fmt.Sprintf("%d 9 * * *", i%60)
		}
		_, err := s.CreateSchedule(ctx, a, input)
		must(t, err)
	}
	_, err := s.CreateSchedule(ctx, b, Schedule{ID: "foreign", Title: "ÄBC secret", Prompt: "secret", Timezone: "UTC", Enabled: true})
	must(t, err)
	f := ScheduleFilter{Query: "äbc", Limit: 10}
	seen := map[string]bool{}
	previousRank := -1
	previousTime := ""
	for {
		page, err := s.ScheduleOverviewPage(ctx, a.Actor, f)
		must(t, err)
		for _, item := range page.Schedules {
			if seen[item.ID] || item.ProjectID != a.ProjectID {
				t.Fatal(item)
			}
			seen[item.ID] = true
			rank := 0
			if !item.Enabled {
				rank = 2
			} else if item.Cron == "" {
				rank = 1
			}
			if rank < previousRank || (rank == 0 && item.NextAt < previousTime) {
				t.Fatal("out of order", item)
			}
			previousRank = rank
			previousTime = item.NextAt
		}
		if page.Next == "" {
			break
		}
		f.After = page.Next
	}
	if len(seen) != 65 {
		t.Fatal("omitted matches", len(seen))
	}
	f = ScheduleFilter{Status: "paused", Limit: 50}
	page, err := s.ScheduleOverviewPage(ctx, a.Actor, f)
	must(t, err)
	if len(page.Schedules) != 5 {
		t.Fatal(page)
	}
	f = ScheduleFilter{Limit: 10}
	page, err = s.ScheduleOverviewPage(ctx, a.Actor, f)
	must(t, err)
	f.After = page.Next
	_, err = s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{After: page.Next, Query: "changed"})
	wantError(t, err, ErrInvalid)
	_, err = s.ScheduleOverviewPage(ctx, b.Actor, f)
	wantError(t, err, ErrInvalid)
	now = now.Add(10 * time.Hour)
	_, err = s.TickSchedules(ctx, 100)
	must(t, err)
	_, err = s.ScheduleOverviewPage(ctx, a.Actor, f)
	wantError(t, err, ErrResync)
	page, err = s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Limit: 10})
	must(t, err)
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, s.RenameProject(ctx, a, p.Revision, "Renamed"))
	_, err = s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{After: page.Next})
	wantError(t, err, ErrResync)
}

func TestScheduleOverviewDoesNotLoadPromptBodies(t *testing.T) {
	store, scope, _ := fixture(t)
	prompt := strings.Repeat("private-prompt-body", 50000)
	_, err := store.CreateSchedule(ctx, scope, Schedule{ID: "large-prompt", Title: "Summary", Prompt: prompt, Timezone: "UTC", Enabled: true})
	must(t, err)
	page, err := store.ScheduleOverviewPage(ctx, scope.Actor, ScheduleFilter{Limit: 50})
	must(t, err)
	raw, err := json.Marshal(page)
	must(t, err)
	if len(raw) > 2048 || bytes.Contains(raw, []byte("prompt"+`":`)) || bytes.Contains(raw, []byte("private-prompt-body")) {
		t.Fatal("overview includes full prompt", len(raw))
	}
	detail, err := store.Schedule(ctx, scope, "large-prompt")
	must(t, err)
	if detail.Prompt != prompt {
		t.Fatal("summary cutover lost editable prompt")
	}
}
