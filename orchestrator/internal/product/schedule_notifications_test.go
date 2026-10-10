package product

import (
	"testing"
)

func TestScheduledOutcomeDeliveryDeduplicatesAndResolvesUnknown(t *testing.T) {
	for _, generic := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduled only", true: "both"}[generic], func(t *testing.T) {
			s, scope, i, c := agentFixture(t)
			ingestContract(t, s, i, c, "native", consumedPayload(c))
			must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{AgentRunOutcome: generic}))
			// Attach the already admitted fixture Prompt to a schedule occurrence. Actual
			// admissions use precisely this immutable notify snapshot.
			task, err := s.CreateSchedule(ctx, scope, Schedule{ID: "notice", Title: "Notice", Prompt: "work", State: "active", TriggerKind: "manual", Timezone: "UTC", Notify: true})
			must(t, err)
			_, err = s.db.Exec(`INSERT INTO schedule_runs(id,schedule_id,project_id,session_id,turn_id,request_id,due_at,created_at,schedule_revision,notify) VALUES('notice-run',?,?,?,?, 'occurrence','',?,1,1)`, task.ID, scope.ProjectID, i.SessionID, c.TurnID, s.timestamp())
			must(t, err)
			task.Notify = false
			must(t, s.UpdateSchedule(ctx, scope, task, false))
			_, err = s.db.Exec(`UPDATE execution_runs SET state='unknown' WHERE id=?`, c.CommandID)
			must(t, err)
			notices, err := s.Notifications(ctx, scope.Actor, "", 20)
			must(t, err)
			if len(notices) != 1 || notices[0].ID != "turn/"+c.TurnID || notices[0].Title != "Agent turn unknown" {
				t.Fatal(notices)
			}
			must(t, s.ReadNotification(ctx, scope.Actor, notices[0].ID))
			_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE id=?`, c.CommandID)
			must(t, err)
			notices, err = s.Notifications(ctx, scope.Actor, "", 20)
			must(t, err)
			if len(notices) != 1 || notices[0].Read || notices[0].Title != "Agent turn completed" {
				t.Fatal(notices)
			}
			must(t, s.ReadNotification(ctx, scope.Actor, notices[0].ID))
			_, err = s.db.Exec(`UPDATE execution_runs SET outcome='completed' WHERE id=?`, c.CommandID)
			must(t, err)
			notices, err = s.Notifications(ctx, scope.Actor, "", 20)
			must(t, err)
			if !notices[0].Read {
				t.Fatal("unchanged outcome reset unread")
			}
		})
	}
}
func TestScheduledQueuedDispositionNotifiesWithoutExecution(t *testing.T) {
	for _, disposition := range []string{"handled", "canceled"} {
		t.Run(disposition, func(t *testing.T) {
			s, scope, _ := fixture(t)
			task, err := s.CreateSchedule(ctx, scope, Schedule{ID: "task", Title: "Task", Prompt: "work", State: "active", TriggerKind: "manual", Timezone: "UTC", Notify: true})
			must(t, err)
			run, err := s.RunSchedule(ctx, scope, task.ID, "once")
			must(t, err)
			before, err := s.Notifications(ctx, scope.Actor, "", 20)
			must(t, err)
			if len(before) != 0 {
				t.Fatal("admission notified success")
			}
			_, err = s.db.Exec(`UPDATE turns SET disposition=? WHERE id=?`, disposition, run.TurnID)
			must(t, err)
			notices, err := s.Notifications(ctx, scope.Actor, "", 20)
			must(t, err)
			if len(notices) != 1 || notices[0].ID != "turn/"+run.TurnID {
				t.Fatal(notices)
			}
		})
	}
}

func TestScheduleCardMetadataAuthorizesAndProjectsLatestRun(t *testing.T) {
	s, scope, foreign := fixture(t)
	task, err := s.CreateSchedule(ctx, scope, Schedule{ID: "task", Title: "Task", Prompt: "private instructions", State: "active", TriggerKind: "manual", Timezone: "UTC", Notify: true})
	must(t, err)
	run, err := s.RunSchedule(ctx, scope, task.ID, "once")
	must(t, err)
	metadata, err := s.ScheduleMetadata(ctx, scope, task.ID)
	must(t, err)
	if metadata.LastState != "queued" || metadata.LastSessionID != run.SessionID || metadata.LastTurnID != run.TurnID {
		t.Fatal(metadata)
	}
	_, err = s.ScheduleMetadata(ctx, foreign, task.ID)
	wantError(t, err, ErrNotFound)
	must(t, s.UpdateSchedule(ctx, scope, task, true))
	metadata, err = s.ScheduleMetadata(ctx, scope, task.ID)
	must(t, err)
	if metadata.State != "deleted" || metadata.LastState != "queued" {
		t.Fatal(metadata)
	}
}
