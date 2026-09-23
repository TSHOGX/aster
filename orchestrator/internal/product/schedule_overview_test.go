package product

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
	first, err := s.ScheduleOverviewPage(ctx, a.Actor, "", "", 100)
	must(t, err)
	if len(first.Schedules) != 100 || first.Next == "" {
		t.Fatal(first)
	}
	second, err := s.ScheduleOverviewPage(ctx, a.Actor, "", first.Next, 100)
	must(t, err)
	if len(second.Schedules) != 5 || second.Next != "" || second.Schedules[0].ID <= first.Next {
		t.Fatal(second)
	}
	for _, item := range first.Schedules {
		if item.ProjectTitle == "" || item.ProjectID == b.ProjectID {
			t.Fatal(item)
		}
	}
	filtered, err := s.ScheduleOverviewPage(ctx, a.Actor, "second", "", 100)
	must(t, err)
	if len(filtered.Schedules) != 52 {
		t.Fatal(len(filtered.Schedules))
	}
	foreign, err := s.ScheduleOverviewPage(ctx, a.Actor, b.ProjectID, "", 100)
	must(t, err)
	if len(foreign.Schedules) != 0 {
		t.Fatal(foreign)
	}
	p, err := s.Project(ctx, Scope{a.Actor, "second"})
	must(t, err)
	must(t, s.DeleteProject(ctx, Scope{a.Actor, "second"}, p.Revision))
	filtered, err = s.ScheduleOverviewPage(ctx, a.Actor, "second", "", 100)
	must(t, err)
	if len(filtered.Schedules) != 0 {
		t.Fatal("deleting project leaked", filtered)
	}
}

func TestScheduleOverviewDoesNotLoadPromptBodies(t *testing.T) {
	store, scope, _ := fixture(t)
	prompt := strings.Repeat("private-prompt-body", 50000)
	_, err := store.CreateSchedule(ctx, scope, Schedule{ID: "large-prompt", Title: "Summary", Prompt: prompt, Timezone: "UTC", Enabled: true})
	must(t, err)
	page, err := store.ScheduleOverviewPage(ctx, scope.Actor, "", "", 50)
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
