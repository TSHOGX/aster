package product

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

func checkFixture(t *testing.T, max int) (*Store, Scope, Incarnation, Schedule) {
	t.Helper()
	s, scope, i, c := agentServiceFixture(t)
	settleCheck(t, s, i, c, "completed")
	task, err := s.CreateSchedule(ctx, scope, Schedule{ID: "check", Title: "Check result", Prompt: "Inspect the event. Finish only if confirmed.", SessionID: i.SessionID, State: "active", TriggerKind: "interval", Cron: "@every 5m", Timezone: "UTC", Notify: true, NotifyPolicy: "on_finish", MaxOccurrences: &max, ExpiresAt: s.now().Add(time.Hour).Format(time.RFC3339)})
	must(t, err)
	return s, scope, i, task
}
func settleCheck(t *testing.T, s *Store, i Incarnation, c agentwire.Frame, outcome string) {
	t.Helper()
	// The normal execution protocol owns consumption and final settlement.
	var disposition string
	must(t, s.reader.QueryRow(`SELECT disposition FROM turns WHERE id=?`, c.TurnID).Scan(&disposition))
	if disposition != "consumed" {
		ingestContract(t, s, i, c, "native", consumedPayload(c))
	}
	ingestContract(t, s, i, c, "native", reconciledPayload(c))
	ingestContract(t, s, i, c, "command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/%s/native/captures/%s.jsonl","digest":"sha256:%064d","size":0,"outcome":%q}`, i.SessionID, c.CommandID, 0, outcome))
}
func startCheck(t *testing.T, s *Store, scope Scope, i Incarnation, request string) agentwire.Frame {
	t.Helper()
	_, err := s.RunSchedule(ctx, scope, "check", request)
	must(t, err)
	c, err := s.agentCommand(ctx, i)
	must(t, err)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	return c
}
func checkPlatform(t *testing.T, s *Store, scope Scope, i Incarnation) (ServiceRequest, Attribution) {
	t.Helper()
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	_, err = s.RegisterGateway(ctx, "model", "gateway", 0, 1)
	must(t, err)
	slot := ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}
	a, err := s.AcquireGatewayRequest(ctx, slot, token)
	must(t, err)
	return slot, a
}
func finishCheckRequest(task Schedule, a Attribution) PlatformScheduleRequest {
	return PlatformScheduleRequest{Action: "execute", Operation: "finish", ID: task.ID, Revision: task.Revision, RequestID: "finish-check", ExpectedRunID: a.RunID, ToolCallID: "finish-call"}
}
func TestBoundedChecksPendingQuietAndLastOccurrenceCanFinish(t *testing.T) {
	s, scope, i, task := checkFixture(t, 2)
	must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{AgentRunOutcome: true}))
	first := startCheck(t, s, scope, i, "first")
	settleCheck(t, s, i, first, "completed")
	notices, err := s.Notifications(ctx, scope.Actor, "", 20)
	must(t, err)
	if len(notices) != 0 {
		t.Fatal("pending check notified", notices)
	}
	last := startCheck(t, s, scope, i, "last")
	current, err := s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.AdmittedCount != 2 || current.State != "exhausted" || current.EndReason != "count" || current.Revision != task.Revision {
		t.Fatal(current)
	}
	slot, a := checkPlatform(t, s, scope, i)
	binding, err := s.PlatformSchedules(ctx, slot, PlatformScheduleRequest{Action: "context"})
	must(t, err)
	if binding.Context.ScheduleID != task.ID || binding.Context.ScheduleRevision != task.Revision {
		t.Fatal(binding)
	}
	request := finishCheckRequest(task, a)
	receipt, err := s.PlatformSchedules(ctx, slot, request)
	must(t, err)
	if receipt.Receipt.Schedule.CompletedByRunID != a.RunID || receipt.Receipt.Schedule.EndReason != "agent_finished" {
		t.Fatal(receipt)
	}
	again, err := s.PlatformSchedules(ctx, slot, request)
	must(t, err)
	if again.Receipt.ReferenceTime != receipt.Receipt.ReferenceTime {
		t.Fatal("finish receipt changed")
	}
	notices, err = s.Notifications(ctx, scope.Actor, "", 20)
	must(t, err)
	if len(notices) != 0 {
		t.Fatal("finish notified before execution completed", notices)
	}
	settleCheck(t, s, i, last, "completed")
	notices, err = s.Notifications(ctx, scope.Actor, "", 20)
	must(t, err)
	if len(notices) != 1 || notices[0].ID != "turn/"+last.TurnID {
		t.Fatal(notices)
	}
	_, err = s.RunSchedule(ctx, scope, task.ID, "too-many")
	wantError(t, err, ErrConflict)
}
func TestBoundedCheckFinishHonorsUserDecisionsAndAmbiguousInputs(t *testing.T) {
	for _, action := range []string{"pause", "cancel", "edit", "mixed", "second-scheduled"} {
		t.Run(action, func(t *testing.T) {
			s, scope, i, task := checkFixture(t, 2)
			c := startCheck(t, s, scope, i, "check")
			slot, a := checkPlatform(t, s, scope, i)
			switch action {
			case "pause":
				task.State = "paused"
				must(t, s.UpdateSchedule(ctx, scope, task, false))
			case "cancel":
				_, err := s.MutateSchedule(ctx, scope, ScheduleMutation{ID: task.ID, RequestID: "cancel", Operation: "cancel", Revision: task.Revision})
				must(t, err)
			case "edit":
				task.Prompt = "new decision"
				must(t, s.UpdateSchedule(ctx, scope, task, false))
			default:
				var source Turn
				if action == "mixed" {
					var err error
					source, err = s.AdmitTurn(ctx, scope, i.SessionID, "steer", "new user intent", false)
					must(t, err)
				} else {
					run, err := s.RunSchedule(ctx, scope, task.ID, "second")
					must(t, err)
					source, err = scanTurn(s.reader.QueryRow(`SELECT `+turnColumns+` FROM turns WHERE id=?`, run.TurnID))
					must(t, err)
				}
				_, err := s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, "mixed-steer"))
				must(t, err)
				steer, err := s.agentCommand(ctx, i)
				must(t, err)
				var payload map[string]string
				must(t, json.Unmarshal(steer.Payload, &payload))
				ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-10-10T00:00:02Z","run_elapsed_ms":2000}`, payload["input_token"], c.CommandID))
			}
			_, err := s.PlatformSchedules(ctx, slot, finishCheckRequest(task, a))
			wantError(t, err, ErrConflict)
			mutation := oneShotMutation("recursive", s.now().Add(time.Hour).Format(time.RFC3339))
			_, err = s.PlatformSchedules(ctx, slot, PlatformScheduleRequest{Action: "execute", Operation: "create", ExpectedRunID: a.RunID, ID: mutation.ID, RequestID: mutation.RequestID, ToolCallID: "recursive", Patch: mutation.Patch})
			wantError(t, err, ErrConflict)
		})
	}
}
func TestBoundedCheckDeadlineAllowsLiveFinishAndFailureStaysStopped(t *testing.T) {
	s, scope, i, task := checkFixture(t, 3)
	c := startCheck(t, s, scope, i, "check")
	slot, a := checkPlatform(t, s, scope, i)
	// A deadline may pass while an admitted execution is still live.
	now := s.now()
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	// Extend only this fixture grant to keep execution authority alive.
	_, err := s.db.Exec(`UPDATE service_grants SET expires_at=?`, s.now().Add(time.Hour).Format(time.RFC3339))
	must(t, err)
	n, err := s.TickSchedules(ctx, 20)
	must(t, err)
	if n != 0 {
		t.Fatal(n)
	}
	current, err := s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.EndReason != "deadline" {
		t.Fatal(current)
	}
	_, err = s.PlatformSchedules(ctx, slot, finishCheckRequest(task, a))
	must(t, err)
	settleCheck(t, s, i, c, "failed")
	current, err = s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.EndReason != "agent_finished" || current.CompletedByRunID != a.RunID {
		t.Fatal(current)
	}
	notices, err := s.Notifications(ctx, scope.Actor, "", 20)
	must(t, err)
	if len(notices) != 1 || notices[0].Title != "Agent turn failed" {
		t.Fatal(notices)
	}
}
func TestBoundedCheckCountAndDeadlineAttention(t *testing.T) {
	for _, kind := range []string{"count", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			s, scope, i, task := checkFixture(t, 1)
			if kind == "count" {
				c := startCheck(t, s, scope, i, "last")
				notices, err := s.Notifications(ctx, scope.Actor, "", 20)
				must(t, err)
				if len(notices) != 0 {
					t.Fatal(notices)
				}
				settleCheck(t, s, i, c, "completed")
			} else {
				now := s.now()
				s.now = func() time.Time { return now.Add(2 * time.Hour) }
				_, err := s.TickSchedules(ctx, 10)
				must(t, err)
			}
			notices, err := s.Notifications(ctx, scope.Actor, "", 20)
			must(t, err)
			if len(notices) != 1 || notices[0].ID != "schedule/"+task.ID {
				t.Fatal(notices)
			}
		})
	}
}

func TestBoundedAutomaticStartCountAndUnknownOverlap(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	max := 2
	task, err := s.CreateSchedule(ctx, scope, Schedule{ID: "bounded", Title: "Bounded", Prompt: "Check", State: "active", TriggerKind: "interval", Cron: "@every 5m", Timezone: "UTC", Notify: true, MaxOccurrences: &max, StartsAt: now.Add(10 * time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), NotifyPolicy: "on_finish"})
	must(t, err)
	if task.NextAt != task.StartsAt {
		t.Fatal("first check not anchored", task)
	}
	now = now.Add(9 * time.Minute)
	n, err := s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 0 {
		t.Fatal(n)
	}
	now = now.Add(time.Minute)
	n, err = s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	now = now.Add(10 * time.Minute)
	n, err = s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 0 {
		t.Fatal("queued check overlapped")
	}
	_, err = s.db.Exec(`UPDATE turns SET disposition='handled' WHERE id IN (SELECT turn_id FROM schedule_runs WHERE schedule_id=?)`, task.ID)
	must(t, err)
	now = now.Add(time.Minute)
	n, err = s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	current, err := s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.State != "exhausted" || current.EndReason != "count" || current.NextAt != "" || current.AdmittedCount != 2 || current.Revision != 1 {
		t.Fatal(current)
	}
	_, err = s.RunSchedule(ctx, scope, task.ID, "over-budget")
	wantError(t, err, ErrConflict)
}
func TestBoundedCheckRejectsUnboundedOrInvalidDefinitions(t *testing.T) {
	for _, change := range []func(*Schedule){
		func(s *Schedule) { s.ExpiresAt = "" }, func(s *Schedule) { s.MaxOccurrences = scheduleValue(101) }, func(s *Schedule) { s.TriggerKind = "manual"; s.Cron = "" }, func(s *Schedule) { s.StartsAt = s.ExpiresAt }, func(s *Schedule) { s.StartsAt = "local time" }, func(s *Schedule) { s.MaxOccurrences = nil },
	} {
		s, scope, _ := fixture(t)
		max := 12
		task := Schedule{ID: "check", Title: "Check", Prompt: "check", State: "active", TriggerKind: "interval", Cron: "@every 5m", Timezone: "UTC", Notify: true, MaxOccurrences: &max, ExpiresAt: s.now().Add(time.Hour).Format(time.RFC3339), NotifyPolicy: "on_finish"}
		change(&task)
		_, err := s.CreateSchedule(ctx, scope, task)
		wantError(t, err, ErrInvalid)
	}
}
func TestBoundedUnknownExecutionNotifiesAndBlocksNextAdmission(t *testing.T) {
	s, scope, i, task := checkFixture(t, 3)
	c := startCheck(t, s, scope, i, "first")
	_, err := s.db.Exec(`UPDATE execution_runs SET state='unknown' WHERE id=?`, c.CommandID)
	must(t, err)
	now := s.now()
	s.now = func() time.Time { return now.Add(10 * time.Minute) }
	n, err := s.TickSchedules(ctx, 20)
	must(t, err)
	if n != 0 {
		t.Fatal("unknown retried", n)
	}
	current, err := s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.AdmittedCount != 1 {
		t.Fatal(current)
	}
	notices, err := s.Notifications(ctx, scope.Actor, "", 20)
	must(t, err)
	if len(notices) != 1 || notices[0].Title != "Agent turn unknown" {
		t.Fatal(notices)
	}
}

func TestBoundedUnknownResolutionUpdatesExistingNotice(t *testing.T) {
	s, scope, i, _ := checkFixture(t, 3)
	c := startCheck(t, s, scope, i, "first")
	_, err := s.db.Exec(`UPDATE execution_runs SET state='unknown' WHERE id=?`, c.CommandID)
	must(t, err)
	must(t, s.ReadNotification(ctx, scope.Actor, "turn/"+c.TurnID))
	_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE id=?`, c.CommandID)
	must(t, err)
	notices, err := s.Notifications(ctx, scope.Actor, "", 20)
	must(t, err)
	if len(notices) != 1 || notices[0].Read || notices[0].Title != "Agent turn completed" {
		t.Fatal(notices)
	}
}
