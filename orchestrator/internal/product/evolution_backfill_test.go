package product

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	"context"
	"harness-platform/orchestrator/internal/sqlitex"
	"harness-platform/orchestrator/internal/studioclient"
)

func (d *dispatchBases) ManualBases(ctx context.Context, p studioclient.Projection) (map[string]evolution.ContentRef, error) {
	return d.Bases(ctx, p)
}
func backfillInput(t *testing.T, s *Store, run string, bindings ...string) EvolutionBackfillInput {
	t.Helper()
	var stamp string
	must(t, s.db.QueryRow("SELECT started_at FROM execution_runs WHERE id=?", run).Scan(&stamp))
	start, err := time.Parse(time.RFC3339Nano, stamp)
	must(t, err)
	budgets := map[string]evolution.Budget{}
	for _, id := range bindings {
		budgets[id] = evolution.Budget{MaxTokens: 20, MaxCostMicros: 20, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1 << 19}
	}
	return EvolutionBackfillInput{RequestID: "preview", ConfigurationRevision: 1, From: sqlitex.FormatTime(start.Add(-time.Second)), Until: sqlitex.FormatTime(start.Add(time.Second)), MaxEpisodes: 4, Budgets: budgets}
}
func TestBackfillPreviewFreezesEvidenceAndRunsWithAutomationDisabled(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	native := finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 1, false)
	input := backfillInput(t, s, command.CommandID, "skills")
	preview, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input)
	must(t, err)
	if preview.State != "preview" || len(preview.Episodes) != 1 || preview.Episodes[0].CaptureState != "missing" {
		t.Fatal(preview)
	}
	if countCorpus(t, s, "corpus_episodes") != 0 || countCorpus(t, s, "corpus_snapshots") != 0 || len(dispatchAssignments(t, s)) != 0 {
		t.Fatal("preview altered automatic frontier or dispatched work")
	}
	ref, content, err := s.BackfillInputContent(ctx, token, "fixture-users", preview.ID, "skills", "corpus")
	must(t, err)
	if evolution.Digest(content) != ref.Digest {
		t.Fatal("preview bytes differ from retained digest")
	}
	if _, raw, err := s.BackfillInputContent(ctx, token, "other", preview.ID, "skills", "corpus"); err == nil || raw != nil {
		t.Fatal("preview content crossed group")
	}
	_, err = s.db.Exec(`CREATE TRIGGER fail_backfill_export BEFORE INSERT ON audit_events WHEN NEW.action='evolution.backfill.export' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if _, raw, err := s.BackfillInputContent(ctx, token, "fixture-users", preview.ID, "skills", "corpus"); err == nil || raw != nil {
		t.Fatal("export escaped failed audit")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_backfill_export")
	must(t, err)
	// Late trace changes the available evidence but never the persisted preview.
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	replay, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input)
	must(t, err)
	if !reflect.DeepEqual(preview, replay) {
		t.Fatal("preview replay changed selection")
	}
	changed := input
	changed.RequestID = "preview-late"
	late, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", changed)
	must(t, err)
	if late.Episodes[0].CaptureState != "complete" || late.Digest == preview.Digest {
		t.Fatal("new preview ignored late evidence", late)
	}
	commandInput := BackfillCommand{RequestID: "execute", Digest: preview.Digest}
	queued, err := s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", commandInput)
	must(t, err)
	if queued.State != "queued" {
		t.Fatal(queued)
	}
	// Actual database reopen must retain both preview and execution intent.
	var seq int
	var name, path string
	must(t, s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	remote := &dispatchTransport{loseReply: true}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: remote, Issuer: "https://app"}
	must(t, worker.prepareBackfill(ctx))
	plans := dispatchAssignments(t, s)
	if len(plans) != 1 || plans[0].Automatic || plans[0].RunID != preview.Runs["skills"] || plans[0].Budget != input.Budgets["skills"] {
		t.Fatal("backfill pins/budget changed", plans)
	}
	if err = worker.dispatch(ctx); err == nil {
		t.Fatal("lost reply absent")
	}
	worker.dispatchCursor = ""
	must(t, worker.dispatch(ctx))
	var corpus evolution.Corpus
	must(t, json.Unmarshal(remote.payloads[plans[0].Pins.InputPorts["corpus"].ID], &corpus))
	if len(corpus.Episodes) != 1 || !corpus.Episodes[0].Truncated {
		t.Fatal("execution silently selected later evidence", corpus)
	}
	completeDependency(t, s, plans[0], "completed")
	frontier, err := s.CorpusFrontier(ctx, token, "fixture-users", "skills", source.ID)
	must(t, err)
	if frontier.Through != 0 || countCorpus(t, s, "corpus_episodes") != 0 {
		t.Fatal("backfill consumed automatic frontier", frontier)
	}
	read, err := s.EvolutionBackfill(ctx, token, "fixture-users", preview.ID)
	must(t, err)
	if read.State != "prepared" {
		t.Fatal(read)
	}
	repeated, err := s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", commandInput)
	must(t, err)
	if repeated.ID != preview.ID || len(dispatchAssignments(t, s)) != 1 {
		t.Fatal("execute replay duplicated batch")
	}
}
func TestBackfillBoundsExclusionsBudgetAndCancellation(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 1, false)
	input := backfillInput(t, s, command.CommandID, "skills")
	tooLarge := input
	tooLarge.MaxEpisodes = 33
	if _, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", tooLarge); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	tooWide := input
	tooWide.Until = time.Now().AddDate(1, 0, 0).Format(time.RFC3339Nano)
	if _, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", tooWide); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	tooExpensive := input
	tooExpensive.Budgets = map[string]evolution.Budget{"skills": input.Budgets["skills"]}
	b := tooExpensive.Budgets["skills"]
	b.MaxTokens = 101
	tooExpensive.Budgets["skills"] = b
	if _, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", tooExpensive); !errors.Is(err, ErrInvalid) {
		t.Fatal("budget exceeded binding", err)
	}
	_, err := s.db.Exec(`INSERT INTO execution_runs(id,project_id,session_id,prompt_id,incarnation_id,owner,state,acknowledged,outcome,started_at,settled_at) SELECT 'extra',project_id,session_id,prompt_id,incarnation_id,owner,'settled',1,'completed',started_at,settled_at FROM execution_runs WHERE id=?`, command.CommandID)
	must(t, err)
	input.MaxEpisodes = 1
	if _, err = s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input); !errors.Is(err, ErrCapacity) {
		t.Fatal("preview silently truncated selected range", err)
	}
	source.PolicyRevision = 2
	source.Exclusions = []string{"executions/extra"}
	_, err = s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "exclude", ExpectedRevision: 1, Source: source})
	must(t, err)
	activateCorpusCollector(t, s, token, source, 2, false)
	input.ConfigurationRevision = 2
	preview, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input)
	must(t, err)
	if len(preview.Episodes) != 1 || preview.Episodes[0].ExecutionID == "extra" {
		t.Fatal("excluded record appeared", preview)
	}
	if _, err = s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", BackfillCommand{RequestID: "forged", Digest: evolution.Digest(nil)}); !errors.Is(err, ErrForbidden) {
		t.Fatal("wrong preview digest accepted", err)
	}
	cancelled, err := s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "cancel", BackfillCommand{RequestID: "cancel", Digest: preview.Digest})
	must(t, err)
	if cancelled.State != "cancelled" {
		t.Fatal(cancelled)
	}
	if _, err = s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", BackfillCommand{RequestID: "execute", Digest: preview.Digest}); !errors.Is(err, ErrConflict) {
		t.Fatal("cancelled preview executed", err)
	}
}
func TestBackfillPreviewAuditFailureAndCurrentAuthority(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 1, false)
	input := backfillInput(t, s, command.CommandID, "skills")
	_, err := s.db.Exec(`CREATE TRIGGER fail_backfill_audit BEFORE INSERT ON audit_events WHEN NEW.action='evolution.backfill.preview' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if preview, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input); err == nil || preview.ID != "" {
		t.Fatal("audit failure returned preview", preview, err)
	}
	var n int
	must(t, s.db.QueryRow("SELECT count(*) FROM generation_input_payloads").Scan(&n))
	if n != 0 {
		t.Fatal("failed preview retained evidence")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_backfill_audit")
	must(t, err)
	preview, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input)
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "revoke", AccountID: scope.Actor, Action: "source.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, err = s.EvolutionBackfill(ctx, token, "fixture-users", preview.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("preview metadata escaped revocation", err)
	}
	if _, err = s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", BackfillCommand{RequestID: "execute", Digest: preview.Digest}); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked preview executed", err)
	}
	if _, raw, err := s.BackfillInputContent(ctx, token, "fixture-users", preview.ID, "skills", "corpus"); !errors.Is(err, ErrForbidden) || raw != nil {
		t.Fatal("revoked preview bytes returned")
	}
	status, err := s.EvolutionBackfillStatus(ctx, token, "fixture-users", preview.ID)
	must(t, err)
	if status.ID != preview.ID || status.Digest != preview.Digest || status.RequestedBy != scope.Actor || len(status.Episodes) != 0 || len(status.Budgets) != 0 || len(status.DownloadInputs) != 0 {
		t.Fatal("cleanup status exposed evidence", status)
	}
	cancelled, err := s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "cancel", BackfillCommand{RequestID: "cancel-after-revoke", Digest: preview.Digest})
	must(t, err)
	if cancelled.State != "cancelled" || len(cancelled.Episodes) != 0 || len(cancelled.Budgets) != 0 {
		t.Fatal("cleanup returned revoked evidence", cancelled)
	}

}

func TestBackfillDependencyBatchUsesPreviewBudgetAndSurvivesDisable(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 1, true, dependencyConfig)
	input := backfillInput(t, s, command.CommandID, "skills", "lessons")
	incomplete := input
	incomplete.Budgets = map[string]evolution.Budget{"lessons": input.Budgets["lessons"]}
	if _, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", incomplete); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing dependency budget accepted", err)
	}
	preview, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input)
	must(t, err)
	_, err = s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", BackfillCommand{RequestID: "execute", Digest: preview.Digest})
	must(t, err)
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	remote := &dependencyTransport{store: s}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: remote, Issuer: "https://app"}
	must(t, worker.prepareBackfill(ctx))
	_, err = s.DisableEvolution(ctx, token, "fixture-users", DisableEvolutionInput{RequestID: "off", ExpectedRevision: 1})
	must(t, err)
	var waiting int
	must(t, s.db.QueryRow("SELECT count(*) FROM generation_preparations WHERE state='waiting'").Scan(&waiting))
	if waiting != 1 {
		t.Fatal("automatic disable cancelled manual backfill")
	}
	must(t, worker.dispatch(ctx))
	root := dispatchAssignments(t, s)[0]
	completeDependency(t, s, root, "completed")
	must(t, worker.resolveDependencies(ctx))
	plans := dispatchAssignments(t, s)
	if len(plans) != 2 {
		t.Fatal("backfill dependency did not resolve", plans)
	}
	for _, a := range plans {
		if a.Automatic || a.Budget != input.Budgets[a.BindingID] || a.BatchID != preview.ID || a.RunID != preview.Runs[a.BindingID] {
			t.Fatal("backfill dependency repinned", a)
		}
	}
	activateCorpusCollector(t, s, token, source, 2, false, dependencyConfig)
	var state string
	must(t, s.db.QueryRow("SELECT state FROM generation_dispatches WHERE run_id=?", preview.Runs["lessons"]).Scan(&state))
	if state != "superseded" {
		t.Fatal("configuration replacement retained unadmitted manual plan", state)
	}
	replay, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", input)
	must(t, err)
	if replay.ID != preview.ID || replay.Digest != preview.Digest {
		t.Fatal("preview response loss could not reconcile after replacement")
	}

}
func TestBackfillPreparationAuditRollbackAndConfigurationReplacement(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	finishCorpusExecution(t, s, incarnation, command)
	activateCorpusCollector(t, s, token, source, 1, false)
	preview, err := s.PreviewEvolutionBackfill(ctx, token, "fixture-users", backfillInput(t, s, command.CommandID, "skills"))
	must(t, err)
	_, err = s.CommandEvolutionBackfill(ctx, token, "fixture-users", preview.ID, "execute", BackfillCommand{RequestID: "execute", Digest: preview.Digest})
	must(t, err)
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: &dispatchTransport{}, Issuer: "https://app"}
	_, err = s.db.Exec(`CREATE TRIGGER fail_backfill_prepare BEFORE INSERT ON audit_events WHEN NEW.action='evolution.backfill.prepared' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if err = worker.prepareBackfill(ctx); err == nil {
		t.Fatal("preparation fault absent")
	}
	if len(dispatchAssignments(t, s)) != 0 || len(triggerItems(t, s, token)) != 0 {
		t.Fatal("failed preparation left partial batch")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_backfill_prepare")
	must(t, err)
	activateCorpusCollector(t, s, token, source, 2, false)
	worker.backfillCursor = ""
	must(t, worker.prepareBackfill(ctx))
	read, err := s.EvolutionBackfill(ctx, token, "fixture-users", preview.ID)
	must(t, err)
	if read.State != "stale" || read.LastError != "configuration_changed" || len(dispatchAssignments(t, s)) != 0 {
		t.Fatal("stale backfill silently upgraded", read)
	}
}
