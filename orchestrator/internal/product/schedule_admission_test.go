package product

import (
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sqlitex"
)

func scheduleValue[T any](v T) *T { return &v }
func oneShotMutation(id, at string) ScheduleMutation {
	return ScheduleMutation{ID: id, RequestID: "create-" + id, Operation: "create", Patch: SchedulePatch{Title: scheduleValue("Check result"), Prompt: scheduleValue("Inspect the result"), TriggerKind: scheduleValue("at"), RunAt: &at, Timezone: scheduleValue("Asia/Shanghai")}}
}
func TestOneShotReceiptConsumptionAndConcurrentReplay(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	input := oneShotMutation("once", now.Add(time.Minute).Format(time.RFC3339))
	original, err := s.MutateSchedule(ctx, scope, input)
	must(t, err)
	now = now.Add(2 * time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.TickSchedules(ctx, 10); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}
	task, err := s.Schedule(ctx, scope, input.ID)
	must(t, err)
	if task.State != "exhausted" || task.EndReason != "trigger_consumed" || task.NextAt != "" || task.Revision != original.Schedule.Revision {
		t.Fatal(task)
	}
	runs, err := s.ScheduleRuns(ctx, scope, input.ID, "", 20)
	must(t, err)
	if len(runs) != 1 || runs[0].ScheduleRevision != 1 || !runs[0].Notify {
		t.Fatal(runs)
	}
	replay, err := s.MutateSchedule(ctx, scope, input)
	must(t, err)
	if !reflect.DeepEqual(replay, original) {
		t.Fatal("receipt changed", replay, original)
	}
	// An edit cannot rearm a consumed trigger.
	_, err = s.MutateSchedule(ctx, scope, ScheduleMutation{ID: input.ID, RequestID: "rearm", Operation: "update", Revision: 1, Patch: SchedulePatch{RunAt: scheduleValue(now.Add(time.Hour).Format(time.RFC3339))}})
	wantError(t, err, ErrConflict)
	input.Patch.Prompt = scheduleValue("different")
	_, err = s.MutateSchedule(ctx, scope, input)
	wantError(t, err, ErrConflict)
	// A manual retry refers to the same canonical one-shot occurrence.
	run, err := s.RunSchedule(ctx, scope, task.ID, "manual")
	must(t, err)
	if run.ID != runs[0].ID {
		t.Fatal("second occurrence", run)
	}
}
func TestScheduleReceiptRollbackCancelAndReopen(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	input := oneShotMutation("once", now.Add(time.Hour).Format(time.RFC3339))
	injected := errors.New("rollback")
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, e := s.mutateSchedule(ctx, tx, scope, input, ScheduleOrigin{})
		if e != nil {
			return e
		}
		return injected
	})
	wantError(t, err, injected)
	_, err = s.Schedule(ctx, scope, input.ID)
	wantError(t, err, ErrNotFound)
	receipt, err := s.MutateSchedule(ctx, scope, input)
	must(t, err)
	canceled, err := s.MutateSchedule(ctx, scope, ScheduleMutation{ID: input.ID, RequestID: "cancel", Operation: "cancel", Revision: 1})
	must(t, err)
	if canceled.Schedule.State != "canceled" || canceled.Admitted {
		t.Fatal(canceled)
	}
	var seq int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	replay, err := s.MutateSchedule(ctx, scope, input)
	must(t, err)
	if !reflect.DeepEqual(receipt, replay) {
		t.Fatal("receipt lost across reopen")
	}
	n, err := s.TickSchedules(ctx, 10)
	must(t, err)
	if n != 0 {
		t.Fatal("canceled trigger revived")
	}
}
func TestScheduleAdmissionRollbackExpiryAndPoisonIsolation(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	input := oneShotMutation("once", now.Add(time.Minute).Format(time.RFC3339))
	receipt, err := s.MutateSchedule(ctx, scope, input)
	must(t, err)
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := s.runSchedule(ctx, tx, scope, receipt.Schedule, "scheduled:"+receipt.Schedule.NextAt, receipt.Schedule.NextAt)
		if e != nil {
			return e
		}
		return ErrInvalid
	})
	wantError(t, err, ErrInvalid)
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM schedule_runs`).Scan(&count))
	if count != 0 {
		t.Fatal("partial admission")
	}
	var sessions int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions))
	if sessions != 0 {
		t.Fatal("partial Session", sessions)
	}
	for _, id := range []string{"poison-a", "poison-b"} {
		_, err = s.CreateSchedule(ctx, scope, Schedule{ID: id, Title: "Bad", Prompt: "work", TriggerKind: "cron", Cron: "* * * * *", Timezone: "UTC", State: "active"})
		must(t, err)
		_, err = s.db.Exec(`UPDATE schedules SET cron='invalid' WHERE id=?`, id)
		must(t, err)
	}
	expiry := oneShotMutation("expired", now.Add(time.Minute).Format(time.RFC3339))
	expiry.Patch.ExpiresAt = scheduleValue(now.Add(2 * time.Minute).Format(time.RFC3339))
	_, err = s.MutateSchedule(ctx, scope, expiry)
	must(t, err)
	now = now.Add(3 * time.Minute)
	n, err := s.TickSchedules(ctx, 50)
	must(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	for _, id := range []string{"poison-a", "poison-b"} {
		task, e := s.Schedule(ctx, scope, id)
		must(t, e)
		if task.State != "paused" || task.LastError == "" {
			t.Fatal(task)
		}
	}
	task, err := s.Schedule(ctx, scope, "expired")
	must(t, err)
	if task.State != "exhausted" || task.EndReason != "deadline" {
		t.Fatal(task)
	}
	n, err = s.TickSchedules(ctx, 50)
	must(t, err)
	if n != 0 {
		t.Fatal(n)
	}
}
func TestScheduleOneShotRejectsAmbiguousAndPastTime(t *testing.T) {
	s, scope, _ := fixture(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for _, at := range []string{"2026-11-01T01:30:00", sqlitex.FormatTime(now), sqlitex.FormatTime(now.Add(-time.Second))} {
		_, err := s.MutateSchedule(ctx, scope, oneShotMutation("once", at))
		wantError(t, err, ErrInvalid)
	}
	input := oneShotMutation("once", "2026-11-01T01:30:00-04:00")
	receipt, err := s.MutateSchedule(ctx, scope, input)
	must(t, err)
	if receipt.Schedule.NextAt != "2026-11-01T05:30:00.000000000Z" {
		t.Fatal(receipt)
	}
}

func TestSourceRetirementKeepsIndependentScheduleAndRedactsReceipts(t *testing.T) {
	s, scope, _ := fixture(t)
	source, err := s.CreateSession(ctx, scope, "source", "Source")
	must(t, err)
	input := oneShotMutation("independent", s.now().Add(time.Hour).Format(time.RFC3339))
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := s.mutateSchedule(ctx, tx, scope, input, ScheduleOrigin{SessionID: source.ID, RunID: "source-run", ToolCallID: "call"})
		return e
	})
	must(t, err)
	_, err = s.MutateSchedule(ctx, scope, ScheduleMutation{ID: input.ID, RequestID: "edit", Operation: "update", Revision: 1, Patch: SchedulePatch{Title: scheduleValue("Renamed")}})
	must(t, err)
	must(t, s.DeleteSession(ctx, scope, source.ID, source.Revision))
	retirement := &ChildRetirement{Store: s}
	must(t, retirement.Step(ctx, Child{ProjectID: scope.ProjectID, ID: source.ID, Kind: "session", Deletion: Requested}))
	task, err := s.Schedule(ctx, scope, input.ID)
	must(t, err)
	if task.State != "active" || task.OriginSessionID != "" || task.OriginRunID != "" {
		t.Fatal(task)
	}
	var private int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM schedule_mutation_receipts WHERE schedule_id=? AND (result<>'{}' OR origin_session_id IS NOT NULL OR origin_run_id IS NOT NULL)`, task.ID).Scan(&private))
	if private != 0 {
		t.Fatal("retired source retained receipt details", private)
	}
}
