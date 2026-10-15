package product

import (
	"errors"
	"sync"
	"testing"
	"time"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/sqlitex"
)

func triggerItems(t *testing.T, s *Store, token string) []EvolutionTrigger {
	t.Helper()
	page, err := s.EvolutionTriggers(ctx, token, "fixture-users", 0)
	must(t, err)
	return page.Items
}
func TestEvolutionTriggersCoalesceNewCorpusAndKeepSiblingRanges(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, true, func(c *evolution.Configuration) {
		sibling := c.Bindings[0]
		sibling.ID = "lessons"
		sibling.Output.Prefix = "lessons"
		c.Bindings = append(c.Bindings, sibling)
	})
	native := finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	initial := triggerItems(t, s, token)
	if len(initial) != 2 || len(initial[0].Ranges) != 1 || len(initial[1].Ranges) != 1 || initial[0].Ranges[0] != initial[1].Ranges[0] {
		t.Fatal(initial)
	}
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	if len(triggerItems(t, s, token)) != 2 {
		t.Fatal("duplicate event occurrences")
	}
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	coalesced := triggerItems(t, s, token)
	if len(coalesced) != 2 {
		t.Fatal(coalesced)
	}
	for i, item := range coalesced {
		if item.ID != initial[i].ID || item.FirstAt != initial[i].FirstAt || item.Ranges[0].Through <= initial[i].Ranges[0].Through {
			t.Fatal("new evidence not coalesced", initial, coalesced)
		}
	}
	snapshot := collectorStatus(t, s, token).SnapshotID
	// A dispatched occurrence becomes immutable; later events queue separately.
	_, err = s.db.Exec("UPDATE evolution_trigger_occurrences SET state='frozen',run_id='run' WHERE id=?", coalesced[0].ID)
	must(t, err)
	if _, err = s.db.Exec("UPDATE evolution_trigger_occurrences SET latest_at='changed' WHERE id=?", coalesced[0].ID); err == nil {
		t.Fatal("frozen occurrence mutable")
	}
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) SELECT 'late-message',project_id,session_id,id,'assistant','late observation',999,created_at FROM turns WHERE id=?`, command.TurnID)
	must(t, err)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	// Simulate successful consumption by only one sibling.
	_, err = s.db.Exec("INSERT INTO corpus_consumption VALUES(?,?,?,?,?,?)", "fixture-users", coalesced[0].BindingID, source.ID, coalesced[0].Ranges[0].Through, snapshot, "run")
	must(t, err)
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	final := triggerItems(t, s, token)
	if len(final) != 3 || final[0].State != "frozen" || final[2].Ranges[0].After != coalesced[0].Ranges[0].Through || final[1].Ranges[0].After != initial[1].Ranges[0].After {
		t.Fatal("sibling consumption coupled", final)
	}
	// Settlement can shrink a pending delta even without a new source event.
	latestSnapshot := collectorStatus(t, s, token).SnapshotID
	_, err = s.db.Exec("INSERT INTO corpus_consumption VALUES(?,?,?,?,?,?)", "fixture-users", final[1].BindingID, source.ID, final[1].Ranges[0].Through, latestSnapshot, "sibling-run")
	must(t, err)
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	settled := triggerItems(t, s, token)
	if len(settled) != 3 || len(settled[1].Ranges) != 0 || settled[1].ID != final[1].ID || len(settled[2].Ranges) != 1 {
		t.Fatal("settled pending ranges stayed stale", settled)
	}
}

func TestEvolutionTriggersMissedScheduleCoalescesWithoutCadenceDrift(t *testing.T) {
	s, _, _, _, token, source := corpusFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	s.now = func() time.Time { return now }
	activateCorpusCollector(t, s, token, source, 1, true, func(c *evolution.Configuration) {
		c.Bindings[0].Trigger = "schedule"
		c.Bindings[0].Schedule = "@every 7m"
		c.Bindings[0].Timezone = "Asia/Shanghai"
	})
	start := now
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	if len(triggerItems(t, s, token)) != 0 {
		t.Fatal("schedule fired at initialization")
	}
	now = start.Add(61 * time.Minute)
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	first := triggerItems(t, s, token)
	if len(first) != 1 || first[0].FirstAt != sqlitex.FormatTime(start.Add(7*time.Minute)) {
		t.Fatal(first)
	}
	var next string
	must(t, s.db.QueryRow("SELECT next_at FROM evolution_trigger_clocks").Scan(&next))
	if next != sqlitex.FormatTime(start.Add(63*time.Minute)) {
		t.Fatal("interval drifted", next)
	}
	now = start.Add(120 * time.Minute)
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	later := triggerItems(t, s, token)
	if len(later) != 1 || later[0].ID != first[0].ID || later[0].FirstAt != first[0].FirstAt {
		t.Fatal("missed ticks replayed", later)
	}
	must(t, s.db.QueryRow("SELECT next_at FROM evolution_trigger_clocks").Scan(&next))
	if next != sqlitex.FormatTime(start.Add(126*time.Minute)) {
		t.Fatal(next)
	}
	_, err := s.DisableEvolution(ctx, token, "fixture-users", DisableEvolutionInput{RequestID: "disable", ExpectedRevision: 1})
	must(t, err)
	if items := triggerItems(t, s, token); items[0].State != "superseded" {
		t.Fatal(items)
	}
	now = start.Add(240 * time.Minute)
	activateCorpusCollector(t, s, token, source, 2, true, func(c *evolution.Configuration) {
		c.Bindings[0].Trigger = "schedule"
		c.Bindings[0].Schedule = "@every 7m"
		c.Bindings[0].Timezone = "Asia/Shanghai"
	})
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	if len(triggerItems(t, s, token)) != 1 {
		t.Fatal("reenable backfilled schedule")
	}
	must(t, s.db.QueryRow("SELECT next_at FROM evolution_trigger_clocks").Scan(&next))
	if next != sqlitex.FormatTime(now.Add(7*time.Minute)) {
		t.Fatal("reenable did not reset schedule", next)
	}
}

func TestEvolutionTriggerAuditFailureRollsBackClockAndPending(t *testing.T) {
	s, _, _, _, token, source := corpusFixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	activateCorpusCollector(t, s, token, source, 1, true, func(c *evolution.Configuration) {
		c.Bindings[0].Trigger = "schedule"
		c.Bindings[0].Schedule = "@every 1m"
		c.Bindings[0].Timezone = "UTC"
	})
	var before string
	must(t, s.db.QueryRow("SELECT next_at FROM evolution_trigger_clocks").Scan(&before))
	now = now.Add(5 * time.Minute)
	_, err := s.db.Exec(`CREATE TRIGGER fail_trigger_audit BEFORE INSERT ON audit_events WHEN NEW.action='evolution.trigger.coalesced' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if err = (&EvolutionTriggerWorker{Store: s}).Step(ctx); err == nil {
		t.Fatal("missing audit fault")
	}
	var after string
	must(t, s.db.QueryRow("SELECT next_at FROM evolution_trigger_clocks").Scan(&after))
	if before != after || len(triggerItems(t, s, token)) != 0 {
		t.Fatal("audit failure advanced scheduler")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_trigger_audit")
	must(t, err)
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	if len(triggerItems(t, s, token)) != 1 {
		t.Fatal("scheduler did not recover")
	}
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "revoke", ExpectedRevision: 1, Source: source})
	must(t, err)
	if err = (&EvolutionTriggerWorker{Store: s}).Step(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked source triggered", err)
	}
}

func TestEvolutionTriggerScheduleActivationRejectsInvalidRecurrence(t *testing.T) {
	s, _, _, _, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, false)
	projection, err := s.EvolutionConfig(ctx, token, "fixture-users")
	must(t, err)
	p, err := s.EvolutionProjection(ctx, token, projection.OperationID)
	must(t, err)
	config := p.Configuration
	config.Revision = 2
	config.AutoEnabled = true
	config.Bindings[0].Trigger = "schedule"
	config.Bindings[0].Schedule = "not a schedule"
	config.Bindings[0].Timezone = "UTC"
	if _, err = s.ActivateEvolutionConfig(ctx, token, "fixture-users", EvolutionActivationInput{RequestID: "bad", ExpectedRevision: 1, Configuration: config}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid recurrence activated", err)
	}
	schedule, _, err := parseRecurrence("0 9 * * *", "America/New_York")
	must(t, err)
	before := time.Date(2026, 3, 7, 14, 0, 0, 0, time.UTC)
	if next := schedule.Next(before); !next.Equal(time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC)) {
		t.Fatal("timezone DST not applied", next)
	}

}

func TestEvolutionTriggersConcurrentTickAndConfigurationReplacement(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, true)
	finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	var workers sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); failures <- (&EvolutionTriggerWorker{Store: s}).Step(ctx) }()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		must(t, err)
	}
	before := triggerItems(t, s, token)
	if len(before) != 1 {
		t.Fatal("concurrent ticks duplicated occurrence", before)
	}
	// A newly active configuration supersedes unfrozen work and establishes its
	// own baseline. Previously frozen generation assignments remain separate.
	activateCorpusCollector(t, s, token, source, 2, true)
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	after := triggerItems(t, s, token)
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].State != "superseded" {
		t.Fatal("configuration silently reused old occurrence", after)
	}
	page, err := s.EvolutionTriggers(ctx, token, "fixture-users", 0)
	must(t, err)
	next, err := s.EvolutionTriggers(ctx, token, "fixture-users", page.Next)
	must(t, err)
	if len(next.Items) != 0 {
		t.Fatal("occurrence cursor repeated rows", next)
	}
	if _, err = s.EvolutionTriggers(ctx, "", "fixture-users", 0); err == nil {
		t.Fatal("anonymous trigger metadata read")
	}
	if _, err = s.EvolutionTriggers(ctx, token, "fixture-users", -1); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative trigger cursor", err)
	}
}
