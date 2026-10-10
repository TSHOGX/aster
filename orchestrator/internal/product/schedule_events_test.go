package product

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScheduleHintsCoverTickManualControlAndDeletion(t *testing.T) {
	s, a, b := fixture(t)
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	task, err := s.CreateSchedule(ctx, a, Schedule{ID: "scheduled", Title: "Task", Prompt: "secret instructions", Cron: "* * * * *", Timezone: "UTC", Enabled: true})
	must(t, err)
	_, err = s.CreateSchedule(ctx, b, Schedule{ID: "foreign", Title: "Secret", Prompt: "foreign instructions", Timezone: "UTC", Enabled: true})
	must(t, err)
	manual, err := s.RunSchedule(ctx, a, task.ID, "once")
	must(t, err)
	now = now.Add(time.Minute)
	_, err = s.TickSchedules(ctx, 10)
	must(t, err)
	_, err = s.db.Exec(`UPDATE turns SET queue_policy='held' WHERE id=?`, manual.TurnID)
	must(t, err)
	task, err = s.Schedule(ctx, a, task.ID)
	must(t, err)
	task.Enabled = false
	must(t, s.UpdateSchedule(ctx, a, task, false))
	task, err = s.Schedule(ctx, a, task.ID)
	must(t, err)
	must(t, s.UpdateSchedule(ctx, a, task, true))
	events, err := s.ScheduleEvents(ctx, a.Actor, 0, 200)
	must(t, err)
	kinds := map[string]bool{}
	for _, e := range events {
		if e.ProjectID != a.ProjectID || e.ScheduleID == "foreign" {
			t.Fatal("foreign hint", e)
		}
		kinds[e.Kind] = true
	}
	for _, kind := range []string{"schedule.created", "schedule.updated", "schedule.deleted", "run.created", "run.updated"} {
		if !kinds[kind] {
			t.Fatal("missing", kind, events)
		}
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "instructions") {
		t.Fatal("prompt leaked", string(raw))
	}
	_, head, err := s.ScheduleEventBounds(ctx, a.Actor)
	must(t, err)
	next, err := s.ScheduleEvents(ctx, a.Actor, head, 200)
	must(t, err)
	if len(next) != 0 {
		t.Fatal(next)
	}
}
func TestScheduleOutboxRetentionAndOwnerIsolation(t *testing.T) {
	s, a, b := fixture(t)
	for n := 0; n < 2060; n++ {
		_, err := s.db.Exec(`INSERT INTO schedule_events(owner_id,project_id,kind) VALUES(?,?,'schedule.updated')`, a.Actor, a.ProjectID)
		must(t, err)
	}
	pruned, head, err := s.ScheduleEventBounds(ctx, a.Actor)
	must(t, err)
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM schedule_events WHERE owner_id=?`, a.Actor).Scan(&count))
	if count != 2048 || pruned == 0 || head <= pruned {
		t.Fatal(count, pruned, head)
	}
	hints, err := s.ScheduleEvents(ctx, b.Actor, 0, 200)
	must(t, err)
	if len(hints) != 0 {
		t.Fatal("foreign events", hints)
	}
}
func TestScheduleBidirectionalWindowsAndDeletedHistoryBoundary(t *testing.T) {
	s, a, b := fixture(t)
	for n := 0; n < 105; n++ {
		_, err := s.CreateSchedule(ctx, a, Schedule{ID: fmt.Sprintf("task-%03d", n), Title: "Task", Prompt: "work", Timezone: "UTC", Enabled: true})
		must(t, err)
	}
	first, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{})
	must(t, err)
	latest, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Anchor: first.Schedules[0].ID})
	must(t, err)
	if latest.Previous != "" {
		t.Fatal("head anchor invented a previous page", latest)
	}
	second, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{After: first.Next})
	must(t, err)
	back, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Before: second.Previous})
	must(t, err)
	if len(back.Schedules) != 50 || back.Schedules[0].ID != first.Schedules[0].ID {
		t.Fatal(back)
	}
	anchor, err := s.ScheduleOverviewPage(ctx, a.Actor, ScheduleFilter{Anchor: second.Schedules[5].ID})
	must(t, err)
	if anchor.Schedules[0].ID != second.Schedules[5].ID {
		t.Fatal(anchor)
	}
	for n := 0; n < 4; n++ {
		_, err := s.RunSchedule(ctx, a, "task-000", fmt.Sprint(n))
		must(t, err)
	}
	head, err := s.ScheduleRunPage(ctx, a, "task-000", "", "", "", 2)
	must(t, err)
	anchored, err := s.ScheduleRunPage(ctx, a, "task-000", "", "", head.Runs[0].ID, 2)
	must(t, err)
	if anchored.Previous != "" {
		t.Fatal("head run anchor invented a previous page", anchored)
	}
	older, err := s.ScheduleRunPage(ctx, a, "task-000", head.Next, "", "", 2)
	must(t, err)
	newer, err := s.ScheduleRunPage(ctx, a, "task-000", "", older.Previous, "", 2)
	must(t, err)
	if len(newer.Runs) != 2 || newer.Runs[0].ID != head.Runs[0].ID {
		t.Fatal(newer)
	}
	_, err = s.db.Exec(`DELETE FROM schedule_runs WHERE id=?`, head.Runs[1].ID)
	must(t, err)
	older, err = s.ScheduleRunPage(ctx, a, "task-000", head.Next, "", "", 2)
	must(t, err)
	if len(older.Runs) != 2 {
		t.Fatal("deleted cursor omitted history", older)
	}
	_, err = s.ScheduleRunPage(ctx, b, "task-000", head.Next, "", "", 2)
	wantError(t, err, ErrNotFound)
}

func TestScheduleHintsAndStatusFollowCorrelatedExecutionOutcomes(t *testing.T) {
	for _, outcome := range []string{"completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			s, scope, i, c := agentFixture(t)
			ingestContract(t, s, i, c, "native", consumedPayload(c))
			_, err := s.CreateSchedule(ctx, scope, Schedule{ID: "scheduled-input", SessionID: i.SessionID, Title: "Scheduled", Prompt: "scheduled guidance", Timezone: "UTC", Enabled: true})
			must(t, err)
			run, err := s.RunSchedule(ctx, scope, "scheduled-input", "once")
			must(t, err)
			source, err := scanTurn(s.reader.QueryRow(`SELECT `+turnColumns+` FROM turns WHERE id=?`, run.TurnID))
			must(t, err)
			_, err = s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, "inject-scheduled"))
			must(t, err)
			steer, err := s.agentCommand(ctx, i)
			must(t, err)
			var payload map[string]string
			must(t, json.Unmarshal(steer.Payload, &payload))
			_, before, err := s.ScheduleEventBounds(ctx, scope.Actor)
			must(t, err)
			ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:02Z","run_elapsed_ms":2000}`, payload["input_token"], c.CommandID))
			runs, err := s.ScheduleRuns(ctx, scope, "scheduled-input", "", 10)
			must(t, err)
			if runs[0].State != "running" {
				t.Fatal("scheduled input status disagrees with execution", runs)
			}
			ingestContract(t, s, i, steer, "command.applied", `{}`)
			ingestContract(t, s, i, c, "native", reconciledPayload(c))
			ingestContract(t, s, i, c, "command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/%s/native/captures/%s.jsonl","digest":"sha256:%064d","size":0,"outcome":%q}`, i.SessionID, c.CommandID, 0, outcome))
			runs, err = s.ScheduleRuns(ctx, scope, "scheduled-input", "", 10)
			must(t, err)
			if runs[0].State != outcome {
				t.Fatal("scheduled outcome disagrees with execution", runs)
			}
			events, err := s.ScheduleEvents(ctx, scope.Actor, before, 100)
			must(t, err)
			if len(events) == 0 {
				t.Fatal("execution transition emitted no hint")
			}
			for _, event := range events {
				if event.Kind != "run.updated" || event.ScheduleID != "scheduled-input" {
					t.Fatal(event)
				}
			}
		})
	}
}
