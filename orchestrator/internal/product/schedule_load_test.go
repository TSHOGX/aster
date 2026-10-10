package product

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestScheduleDueLoadLeavesUrgentAndIngestCapacity(t *testing.T) {
	s, a, b := fixture(t)
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for _, scope := range []Scope{a, b} {
		for n := 0; n < 100; n++ {
			_, err := s.CreateSchedule(ctx, scope, Schedule{ID: fmt.Sprintf("%s-%03d", scope.ProjectID, n), Title: "Load check", Prompt: "work", State: "active", TriggerKind: "at", RunAt: now.Add(time.Minute).Format(time.RFC3339), Timezone: "UTC"})
			must(t, err)
		}
	}
	now = now.Add(2 * time.Minute)
	before, err := s.ScheduleDiagnostics(ctx)
	must(t, err)
	if before.Eligible != 200 || before.OldestEligibleAgeNanos != int64(time.Minute) {
		t.Fatal(before)
	}
	started := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 16; n++ {
				_, e := s.TickSchedules(ctx, 16)
				if e != nil {
					errs <- e
					return
				}
			}
			errs <- nil
		}()
	}
	for _, class := range []writeClass{urgentWrite, ingestWrite} {
		class := class
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 25; n++ {
				if e := s.writeClass(ctx, class, a.ProjectID, 128, func(tx *sql.Tx) error { var one int; return tx.QueryRow(`SELECT 1`).Scan(&one) }); e != nil {
					errs <- e
					return
				}
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}
	after, err := s.ScheduleDiagnostics(ctx)
	must(t, err)
	if after.Admissions != 200 || after.Eligible != 0 || after.Outstanding != 200 || after.AdmissionFailures != 0 {
		t.Fatal(after)
	}
	writer := s.WriterStats()
	if writer["stop_control"].Admitted < 25 || writer["ingest"].Admitted < 25 || writer["stop_control"].Rejected != 0 || writer["ingest"].Rejected != 0 {
		t.Fatal(writer)
	}
	t.Logf("200 due tasks: wall=%s max_due_lag=%s urgent_max_wait=%s ingest_max_wait=%s", time.Since(started), time.Duration(after.MaxAdmissionLagNanos), time.Duration(writer["stop_control"].MaxWaitNanos), time.Duration(writer["ingest"].MaxWaitNanos))
}
