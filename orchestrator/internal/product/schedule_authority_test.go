package product

import (
	"database/sql"
	"testing"
	"time"
)

func TestScheduleRevocationPausesDueWorkAndRetainsReduction(t *testing.T) {
	for _, action := range []ProductAction{ActionSchedulesManage, ActionAgentsRun} {
		t.Run(string(action), func(t *testing.T) {
			s, scope, _ := fixture(t)
			clock := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return clock }
			schedule, err := s.CreateSchedule(ctx, scope, Schedule{ID: "scheduled", Title: "Task", Prompt: "do work", TriggerKind: "interval", Cron: "@every 1m", Timezone: "UTC", State: "active"})
			must(t, err)
			fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{action: PolicyDeny})
			_, err = s.RunSchedule(ctx, scope, schedule.ID, "manual")
			wantError(t, err, ErrForbidden)
			clock = clock.Add(2 * time.Minute)
			count, err := s.TickSchedules(ctx, 10)
			must(t, err)
			if count != 0 {
				t.Fatal("revoked schedule admitted", count)
			}
			current, err := s.Schedule(ctx, scope, schedule.ID)
			must(t, err)
			if current.State != "paused" || current.LastError != "permission_revoked" || current.NextAt != "" {
				t.Fatal(current)
			}
			var sessions, runs int
			must(t, s.reader.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions))
			must(t, s.reader.QueryRow(`SELECT count(*) FROM schedule_runs`).Scan(&runs))
			if sessions != 0 || runs != 0 {
				t.Fatal("denial created work", sessions, runs)
			}
			fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{action: PolicyAllow})
			count, err = s.TickSchedules(ctx, 10)
			must(t, err)
			if count != 0 {
				t.Fatal("restoring grant resumed task", count)
			}
			active := "active"
			_, err = s.MutateSchedule(ctx, scope, ScheduleMutation{ID: schedule.ID, RequestID: "resume", Operation: "update", Revision: current.Revision, Patch: SchedulePatch{State: &active}})
			must(t, err)
			current, err = s.Schedule(ctx, scope, schedule.ID)
			must(t, err)
			fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionSchedulesManage: PolicyDeny})
			paused := "paused"
			reduced, err := s.MutateSchedule(ctx, scope, ScheduleMutation{ID: schedule.ID, RequestID: "pause", Operation: "update", Revision: current.Revision, Patch: SchedulePatch{State: &paused}})
			must(t, err)
			replacement := "changed prompt"
			_, err = s.MutateSchedule(ctx, scope, ScheduleMutation{ID: schedule.ID, RequestID: "disguised-edit", Operation: "update", Revision: reduced.Schedule.Revision, Patch: SchedulePatch{State: &paused, Prompt: &replacement}})
			wantError(t, err, ErrForbidden)
			_, err = s.MutateSchedule(ctx, scope, ScheduleMutation{ID: schedule.ID, RequestID: "cancel", Operation: "cancel", Revision: reduced.Schedule.Revision})
			must(t, err)
		})
	}
}

func TestGatewayRevocationSeparatesNewRequestsFromSettlement(t *testing.T) {
	s, scope, i, _ := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	_, err = s.RegisterGateway(ctx, "model", "gateway", 0, 2)
	must(t, err)
	request := ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}
	_, err = s.AcquireGatewayRequest(ctx, request, token)
	must(t, err)
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionAgentsRun: PolicyDeny})
	fresh := request
	fresh.Slot = 1
	_, err = s.AcquireGatewayRequest(ctx, fresh, token)
	wantError(t, err, ErrForbidden)
	must(t, s.CheckGatewayRequest(ctx, request))
	must(t, s.RenewService(ctx, i, "model", time.Hour))
	// Browser password setup does not invalidate this autonomous request.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET must_change_password=1 WHERE id=?`, scope.Actor)
		return err
	}))
	must(t, s.CheckGatewayRequest(ctx, request))
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=?`, scope.Actor)
		return err
	}))
	wantError(t, s.CheckGatewayRequest(ctx, request), ErrForbidden)
	wantError(t, s.RenewService(ctx, i, "model", time.Hour), ErrForbidden)
	must(t, s.ReleaseGatewayRequest(ctx, request))
}

func TestPlatformScheduleRechecksGrantOnExistingGateway(t *testing.T) {
	s, _, token := PlatformFixtureForTest(t)
	_, err := s.RegisterGateway(ctx, "model", "gateway", 0, 1)
	must(t, err)
	slot := ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}
	attributed, err := s.AcquireGatewayRequest(ctx, slot, token)
	must(t, err)
	definition := oneShotMutation("future", s.now().Add(time.Hour).Format(time.RFC3339))
	request := PlatformScheduleRequest{Action: "execute", Operation: "create", ID: definition.ID, RequestID: definition.RequestID, ExpectedRunID: attributed.RunID, ToolCallID: "call", Patch: definition.Patch}
	created, err := s.PlatformSchedules(ctx, slot, request)
	must(t, err)
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionSchedulesManage: PolicyDeny})
	_, err = s.PlatformSchedules(ctx, slot, request)
	wantError(t, err, ErrForbidden)
	current, err := s.PlatformSchedules(ctx, slot, PlatformScheduleRequest{Action: "context"})
	must(t, err)
	for _, operation := range current.Context.Operations {
		if operation == "create" || operation == "update" {
			t.Fatal("context offers denied operation", current.Context)
		}
	}
	request.Operation = "cancel"
	request.RequestID = "cancel"
	request.Revision = created.Receipt.Schedule.Revision
	request.Patch = SchedulePatch{}
	_, err = s.PlatformSchedules(ctx, slot, request)
	must(t, err)
}

func TestSchedulePermissionPauseIsAtomic(t *testing.T) {
	s, scope, _ := fixture(t)
	clock := s.now()
	s.now = func() time.Time { return clock }
	schedule, err := s.CreateSchedule(ctx, scope, Schedule{ID: "scheduled", Title: "Task", Prompt: "do work", TriggerKind: "interval", Cron: "@every 1m", Timezone: "UTC", State: "active"})
	must(t, err)
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionSchedulesManage: PolicyDeny})
	clock = clock.Add(2 * time.Minute)
	_, err = s.db.Exec(`CREATE TRIGGER deny_schedule_pause BEFORE UPDATE OF state ON schedules BEGIN SELECT RAISE(ABORT,'injected pause failure'); END`)
	must(t, err)
	_, err = s.TickSchedules(ctx, 10)
	if err == nil {
		t.Fatal("pause fault hidden")
	}
	current, err := s.Schedule(ctx, scope, schedule.ID)
	must(t, err)
	if current.State != "active" || current.LastError != "" {
		t.Fatal("partial pause committed", current)
	}
	_, err = s.db.Exec(`DROP TRIGGER deny_schedule_pause`)
	must(t, err)
	count, err := s.TickSchedules(ctx, 10)
	must(t, err)
	if count != 0 {
		t.Fatal(count)
	}
	current, err = s.Schedule(ctx, scope, schedule.ID)
	must(t, err)
	if current.State != "paused" || current.LastError != "permission_revoked" {
		t.Fatal(current)
	}
}
