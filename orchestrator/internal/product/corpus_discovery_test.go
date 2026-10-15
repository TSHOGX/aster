package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/agentwire"
)

func activateCorpusCollector(t *testing.T, s *Store, token string, source evolution.SourceBinding, revision int64, enabled bool, mutations ...func(*evolution.Configuration)) {
	t.Helper()
	ref := evolution.Revision{ID: "ref", Revision: 1, Digest: evolution.Digest([]byte("ref"))}
	budget := evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1 << 20}
	config := evolution.Configuration{GroupID: "fixture-users", Revision: revision, AutoEnabled: enabled, Sources: []evolution.SourceBinding{source}, Budget: budget,
		Bindings: []evolution.Binding{{ID: "skills", Enabled: true, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{source.ID}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Budget: budget, Trigger: "event"}}}
	for _, mutate := range mutations {
		mutate(&config)
	}
	pending, err := s.ActivateEvolutionConfig(ctx, token, config.GroupID, EvolutionActivationInput{RequestID: fmt.Sprintf("activate-%d", revision), ExpectedRevision: revision - 1, Configuration: config})
	must(t, err)
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, participant, pending.DesiredDigest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
}
func finishCorpusExecution(t *testing.T, s *Store, incarnation Incarnation, command agentwire.Frame) string {
	t.Helper()
	native := `{"type":"message","message":{"role":"assistant","content":"Verified task completed"}}` + "\n"
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":%d}`, command.CommandID, evolution.Digest([]byte(native)), len(native))
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 2, "command.completed", payload))
	must(t, err)
	return native
}
func collectorStatus(t *testing.T, s *Store, token string) CorpusCollectorStatus {
	t.Helper()
	all, err := s.CorpusCollectors(ctx, token, "fixture-users")
	must(t, err)
	if len(all) != 1 {
		t.Fatal(all)
	}
	return all[0]
}
func countCorpus(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	switch table {
	case "corpus_episodes", "corpus_snapshots", "corpus_consumption":
	default:
		t.Fatal("unsupported test table")
	}
	must(t, s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}
func TestCorpusDiscoverySealsAtomicallyAndRecoversLateEvidence(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, true)
	before := collectorStatus(t, s, token)
	native := finishCorpusExecution(t, s, incarnation, command)
	_, err := s.db.Exec(`CREATE TRIGGER fail_collector_seal BEFORE INSERT ON corpus_snapshots BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if err = (&CorpusCollector{Store: s}).Step(ctx); err == nil {
		t.Fatal("missing sealing failure")
	}
	if status := collectorStatus(t, s, token); status != before {
		t.Fatal("failed seal advanced capture cursor", status)
	}
	if countCorpus(t, s, "corpus_episodes") != 0 {
		t.Fatal("failed seal retained capture")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_collector_seal")
	must(t, err)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	first := collectorStatus(t, s, token)
	if first.AfterEvent <= before.AfterEvent || first.SealedThrough == 0 || first.SnapshotID == "" {
		t.Fatal(first)
	}
	if countCorpus(t, s, "corpus_episodes") != 1 || countCorpus(t, s, "corpus_snapshots") != 1 || countCorpus(t, s, "corpus_consumption") != 0 {
		t.Fatal("capture/seal changed goal consumption frontier")
	}
	raw, err := s.CorpusSnapshotContent(ctx, token, "fixture-users", first.SnapshotID)
	must(t, err)
	var corpus evolution.Corpus
	must(t, json.Unmarshal(raw, &corpus))
	if len(corpus.Episodes) != 1 || !corpus.Episodes[0].Truncated {
		t.Fatal(corpus)
	}
	// Reopen the actual database with no saved worker token or in-memory cursor.
	var seq int
	var name, path string
	must(t, s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	late := collectorStatus(t, s, token)
	if late.SnapshotID == first.SnapshotID || late.SealedThrough <= first.SealedThrough || late.AfterEvent <= first.AfterEvent {
		t.Fatal("late capture not discovered", late)
	}
	raw, err = s.CorpusSnapshotContent(ctx, token, "fixture-users", late.SnapshotID)
	must(t, err)
	must(t, json.Unmarshal(raw, &corpus))
	if len(corpus.Episodes) != 1 || corpus.Episodes[0].Truncated {
		t.Fatal(corpus)
	}
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	if countCorpus(t, s, "corpus_episodes") != 2 || countCorpus(t, s, "corpus_snapshots") != 2 {
		t.Fatal("restart replay duplicated artifacts")
	}
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) SELECT 'late-message',project_id,session_id,id,'assistant','late observation',999,created_at FROM turns WHERE id=?`, command.TurnID)
	must(t, err)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	if countCorpus(t, s, "corpus_episodes") != 3 || countCorpus(t, s, "corpus_snapshots") != 3 {
		t.Fatal("late message projection not discovered")
	}
}

func TestCorpusDiscoveryDoesNotBackfillHistoricalLateCapture(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	native := finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 1, true)
	before := collectorStatus(t, s, token)
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	after := collectorStatus(t, s, token)
	if after.AfterEvent <= before.AfterEvent || countCorpus(t, s, "corpus_episodes") != 0 || countCorpus(t, s, "corpus_snapshots") != 0 {
		t.Fatal("historical execution was automatically backfilled", after)
	}
}

func TestCorpusDiscoveryDisableReenableAndUnchangedRevision(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, true)
	before := collectorStatus(t, s, token)
	native := finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 2, true)
	retained := collectorStatus(t, s, token)
	if retained.Baseline != before.Baseline || retained.AfterEvent != before.AfterEvent {
		t.Fatal("reactivation discarded pending evidence", retained)
	}
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	if countCorpus(t, s, "corpus_episodes") != 1 {
		t.Fatal("pending event lost")
	}
	_, err := s.DisableEvolution(ctx, token, "fixture-users", DisableEvolutionInput{RequestID: "disable", ExpectedRevision: 2})
	must(t, err)
	all, err := s.CorpusCollectors(ctx, token, "fixture-users")
	must(t, err)
	if len(all) != 0 {
		t.Fatal("disabled collector retained", all)
	}
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	activateCorpusCollector(t, s, token, source, 3, true)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	if countCorpus(t, s, "corpus_episodes") != 1 || countCorpus(t, s, "corpus_snapshots") != 1 {
		t.Fatal("reenabling replayed history")
	}
}

func TestCorpusDiscoveryRevocationAndUnrelatedPages(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, true)
	for i := 0; i < 70; i++ {
		_, err := s.db.Exec("INSERT INTO corpus_discovery_events(execution_id,kind) VALUES(?,'terminal')", fmt.Sprintf("unrelated-%d", i))
		must(t, err)
	}
	finishCorpusExecution(t, s, incarnation, command)
	_, err := s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "revoke", AccountID: scope.Actor, Action: "source.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	before := collectorStatus(t, s, token)
	if err = (&CorpusCollector{Store: s}).Step(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked evidence authority used", err)
	}
	if collectorStatus(t, s, token) != before {
		t.Fatal("revoked collector advanced")
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "restore", AccountID: scope.Actor, Action: "source.read", Effect: "allow", ExpectedRevision: 2})
	must(t, err)
	for i := 0; i < 3; i++ {
		must(t, (&CorpusCollector{Store: s}).Step(ctx))
	}
	if countCorpus(t, s, "corpus_episodes") != 1 || countCorpus(t, s, "corpus_snapshots") != 1 {
		t.Fatal("unrelated pages starved authorized evidence")
	}
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "source-revoke", ExpectedRevision: 1, Source: source})
	must(t, err)
	if err = (&CorpusCollector{Store: s}).Step(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked source policy used", err)
	}
}

func TestCorpusDiscoveryConcurrentWorkersSealOnce(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, true)
	finishCorpusExecution(t, s, incarnation, command)
	var workers sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); failures <- (&CorpusCollector{Store: s}).Step(ctx) }()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		must(t, err)
	}
	if countCorpus(t, s, "corpus_episodes") != 1 || countCorpus(t, s, "corpus_snapshots") != 1 {
		t.Fatal("concurrent collector duplicated evidence")
	}
}
