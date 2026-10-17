package product

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func TestManualConfiguredBatchUsesScopedOperatorAndFrozenReplay(t *testing.T) {
	s, _, incarnation, command, adminToken, source := corpusFixture(t)
	activateCorpusCollector(t, s, adminToken, source, 1, false)
	finishCorpusExecution(t, s, incarnation, command)
	_, err := s.CaptureCorpusEpisode(ctx, adminToken, "fixture-users", CorpusCaptureInput{RequestID: "manual-capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	created, err := s.CreateAccount(ctx, adminToken, AccountCreate{RequestID: "operator", Username: "operator", DisplayName: "Operator", Role: "user", GroupIDs: &[]string{"fixture-users"}})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "operator", created.TemporaryPassword)
	must(t, err)
	_, token, err := s.ChangePassword(ctx, temporary, "", "permanent-operator-password", true)
	must(t, err)
	for _, action := range []string{"config.read", "run.start", "source.read"} {
		_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "fixture-users", EvolutionGroupGrantInput{RequestID: "operator-" + strings.ReplaceAll(action, ".", "-"), AccountID: created.User.ID, Action: action, Effect: "allow"})
		must(t, err)
	}
	must(t, grantFixture(s, ctx, adminToken, "account", created.User.ID, "library", "volume.read", "allow", 0))
	input := EvolutionManualInput{RequestID: "manual", ConfigurationRevision: 1, Budgets: map[string]evolution.Budget{"skills": {MaxTokens: 20, MaxCostMicros: 20, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1 << 19}}}
	_, found, p, err := s.BeginManualEvolution(ctx, token, "fixture-users", input)
	must(t, err)
	if found || p.Configuration.Revision != 1 {
		t.Fatal(found, p)
	}
	base := evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}
	result, err := s.StartManualEvolution(ctx, token, "fixture-users", "https://app", input, map[string]evolution.ContentRef{"library": base})
	must(t, err)
	plans := dispatchAssignments(t, s)
	if len(plans) != 1 || plans[0].Automatic || plans[0].RunID != result.Runs["skills"] || plans[0].Requester.Subject != created.User.ID || plans[0].Budget != input.Budgets["skills"] {
		t.Fatal(plans, result)
	}
	if _, err = s.EvolutionDraft(ctx, token, "fixture-users"); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.SaveEvolutionDraft(ctx, token, "fixture-users", EvolutionDraftInput{RequestID: "cannot-configure", Configuration: evolution.Configuration{GroupID: "fixture-users"}}); !errors.Is(err, ErrForbidden) {
		t.Fatal("operator gained config authority", err)
	}
	if _, err = s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "cannot-capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID}); !errors.Is(err, ErrForbidden) {
		t.Fatal("operator gained capture authority", err)
	}
	replay, found, _, err := s.BeginManualEvolution(ctx, token, "fixture-users", input)
	must(t, err)
	if !found || !reflect.DeepEqual(result, replay) {
		t.Fatal("lost reply changed batch", result, replay)
	}
	next := input
	next.RequestID = "busy"
	if _, err = s.StartManualEvolution(ctx, token, "fixture-users", "https://app", next, map[string]evolution.ContentRef{"library": base}); !errors.Is(err, ErrCapacity) {
		t.Fatal("overlapping manual run", err)
	}
	var sequence int
	var name, path string
	must(t, s.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	remote := &dispatchTransport{loseReply: true}
	worker := &EvolutionDispatcher{Store: s, EvolutionSpace: remote, Studio: &dispatchBases{ref: base}, Issuer: "https://app"}
	if err = worker.dispatch(ctx); err == nil {
		t.Fatal("lost admission reply absent")
	}
	worker.dispatchCursor = ""
	must(t, worker.dispatch(ctx))
	if len(remote.assignments) != 1 {
		t.Fatal("manual admission duplicated")
	}
	completeDependency(t, s, plans[0], "completed")
	frontier, err := s.CorpusFrontier(ctx, adminToken, "fixture-users", "skills", source.ID)
	must(t, err)
	if frontier.Through == 0 || frontier.RunID != result.Runs["skills"] {
		t.Fatal("successful manual delta did not consume own frontier", frontier)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "fixture-users", EvolutionGroupGrantInput{RequestID: "revoke-run", AccountID: created.User.ID, Action: "run.start", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, _, _, err = s.BeginManualEvolution(ctx, token, "fixture-users", input); !errors.Is(err, ErrForbidden) {
		t.Fatal("receipt bypassed current denial", err)
	}
}

func TestManualBatchDependenciesAuditRollbackAndDisable(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	activateCorpusCollector(t, s, token, source, 1, false, dependencyConfig)
	finishCorpusExecution(t, s, incarnation, command)
	_, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	budget := evolution.Budget{MaxTokens: 20, MaxCostMicros: 20, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1 << 19}
	input := EvolutionManualInput{RequestID: "dependent", ConfigurationRevision: 1, Budgets: map[string]evolution.Budget{"lessons": budget}}
	base := evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}
	bases := map[string]evolution.ContentRef{"library": base}
	if _, err = s.StartManualEvolution(ctx, token, "fixture-users", "https://app", input, bases); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing prerequisite accepted", err)
	}
	input.Budgets["skills"] = budget
	_, err = s.db.Exec(`CREATE TRIGGER manual_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='evolution.manual.queued' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if _, err = s.StartManualEvolution(ctx, token, "fixture-users", "https://app", input, bases); err == nil {
		t.Fatal("audit failure ignored")
	}
	if len(dispatchAssignments(t, s)) != 0 || countCorpus(t, s, "corpus_snapshots") != 0 {
		t.Fatal("failed admission leaked effects")
	}
	_, err = s.db.Exec("DROP TRIGGER manual_audit_fault")
	must(t, err)
	result, err := s.StartManualEvolution(ctx, token, "fixture-users", "https://app", input, bases)
	must(t, err)
	if len(result.Runs) != 2 || len(dispatchAssignments(t, s)) != 1 {
		t.Fatal("dependency did not wait", result)
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error { return clearEvolutionTriggers(ctx, tx, "fixture-users", true) }))
	remote := &dependencyTransport{store: s}
	worker := &EvolutionDispatcher{Store: s, Studio: &dispatchBases{ref: base}, EvolutionSpace: remote, Issuer: "https://app"}
	must(t, worker.dispatch(ctx))
	first := dispatchAssignments(t, s)[0]
	completeDependency(t, s, first, "completed")
	must(t, worker.resolveDependencies(ctx))
	plans := dispatchAssignments(t, s)
	if len(plans) != 2 {
		t.Fatal("manual dependency did not resolve", plans)
	}
	for _, a := range plans {
		if a.BindingID == "lessons" {
			if a.RunID != result.Runs["lessons"] || a.Automatic || len(a.Pins.Dependencies) != 1 || a.Pins.Dependencies[0].RunID != first.RunID {
				t.Fatal("manual dependency changed lineage", a)
			}
		}
	}
}

func TestManualBatchRejectsSourceRevocationAndInvalidBudgetsWithoutPins(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, false)
	finishCorpusExecution(t, s, incarnation, command)
	_, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	base := evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}
	input := EvolutionManualInput{RequestID: "manual", ConfigurationRevision: 1, Budgets: map[string]evolution.Budget{"skills": {MaxTokens: 1000000, MaxCostMicros: 20, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1 << 19}}}
	if _, err = s.StartManualEvolution(ctx, token, "fixture-users", "https://app", input, map[string]evolution.ContentRef{"library": base}); !errors.Is(err, ErrInvalid) {
		t.Fatal("budget ceiling bypassed", err)
	}
	budget := input.Budgets["skills"]
	budget.MaxTokens = 20
	input.Budgets["skills"] = budget
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "deny-evidence", AccountID: scope.Actor, Action: "source.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, err = s.StartManualEvolution(ctx, token, "fixture-users", "https://app", input, map[string]evolution.ContentRef{"library": base}); !errors.Is(err, ErrForbidden) {
		t.Fatal("source evidence denied", err)
	}
	if countCorpus(t, s, "corpus_snapshots") != 0 || len(dispatchAssignments(t, s)) != 0 {
		t.Fatal("denial leaked frozen input/dispatch")
	}
}
