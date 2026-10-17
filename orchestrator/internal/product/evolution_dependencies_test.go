package product

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type dependencyTransport struct {
	dispatchTransport
	store *Store
	reads int
	fail  bool
}

func (d *dependencyTransport) GenerationOutput(ctx context.Context, scope evolution.GenerationOutputScope) (evolution.GenerationOutput, error) {
	d.reads++
	if d.fail {
		return evolution.GenerationOutput{}, errors.New("output connection unavailable")
	}
	var raw, grantRaw []byte
	if err := d.store.reader.QueryRowContext(ctx, "SELECT assignment,grant FROM generation_authorizations WHERE run_id=?", scope.RunID).Scan(&raw, &grantRaw); err != nil {
		return evolution.GenerationOutput{}, err
	}
	var a evolution.GenerationAssignment
	var grant evolution.GenerationGrant
	if err := json.Unmarshal(raw, &a); err != nil {
		return evolution.GenerationOutput{}, err
	}
	if err := json.Unmarshal(grantRaw, &grant); err != nil {
		return evolution.GenerationOutput{}, err
	}
	body := []byte(`"verified upstream output"`)
	ref := evolution.ArtifactRef{ID: "output-" + scope.RunID, GroupID: scope.GroupID, SchemaDigest: evolution.Digest([]byte(`{"type":"string"}`)), Digest: evolution.Digest(body), Size: int64(len(body)), MediaType: "application/json", GrantID: grant.ID, PolicyRevision: a.Pins.SourcePolicyRevision}
	return evolution.GenerationOutput{Scope: scope, Workflow: a.Pins.Workflow, Artifact: ref}, nil
}
func dependencyConfig(c *evolution.Configuration) {
	parent := c.Bindings[0]
	child := parent
	child.ID = "lessons"
	child.Output.Prefix = "lessons"
	child.DependsOn = []string{parent.ID}
	child.Inputs = map[string]evolution.BindingInput{"evidence": {Dependency: &evolution.BindingDependency{BindingID: parent.ID, OutputPort: "value"}}}
	// Consumers intentionally precede parents in the authored document.
	c.Bindings = []evolution.Binding{child, parent}
	c.Budget.MaxTokens *= 2
	c.Budget.MaxCostMicros *= 2
}
func completeDependency(t *testing.T, s *Store, a evolution.GenerationAssignment, state string) {
	t.Helper()
	grant, err := s.AuthorizeGeneration(ctx, a)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: state}))
	must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 1, Tokens: 1, CostMicros: 1}))
}
func TestGenerationDependenciesFreezeExactBatchAndWaitForSuccessfulOutput(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	activateCorpusCollector(t, s, token, source, 1, true, dependencyConfig)
	finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	remote := &dependencyTransport{store: s}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, EvolutionSpace: remote, Issuer: "https://app"}
	must(t, worker.prepare(ctx))
	frozen := dispatchAssignments(t, s)
	if len(frozen) != 1 || frozen[0].BindingID != "skills" {
		t.Fatal("dependent run admitted without output", frozen)
	}
	var planRaw []byte
	must(t, s.db.QueryRow("SELECT assignment FROM generation_preparations").Scan(&planRaw))
	var planned evolution.GenerationAssignment
	must(t, json.Unmarshal(planRaw, &planned))
	if planned.BatchID != frozen[0].BatchID || planned.Pins.Base != frozen[0].Pins.Base {
		t.Fatal("dependent pins not frozen with batch")
	}
	must(t, worker.resolveDependencies(ctx))
	if remote.reads != 0 {
		t.Fatal("read nonexistent upstream output")
	}
	must(t, worker.dispatch(ctx))
	worker.dependencyCursor = ""
	must(t, worker.resolveDependencies(ctx))
	if remote.reads != 0 {
		t.Fatal("read active upstream output")
	}
	completeDependency(t, s, frozen[0], "completed")
	// A new base and a restarted worker cannot repin the waiting consumer.
	bases.ref.CommitID = strings.Repeat("b", 64)
	var seq int
	var name, path string
	must(t, s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path))
	must(t, s.Close())
	var reopenErr error
	s, reopenErr = Open(ctx, path)
	must(t, reopenErr)
	t.Cleanup(func() { s.Close() })
	remote.store = s
	worker = &EvolutionDispatcher{Store: s, Studio: bases, EvolutionSpace: remote, Issuer: "https://app"}
	remote.fail = true
	if err := worker.resolveDependencies(ctx); err == nil {
		t.Fatal("missing output outage")
	}
	if len(dispatchAssignments(t, s)) != 1 {
		t.Fatal("outage admitted consumer")
	}
	remote.fail = false
	worker.dependencyCursor = ""
	must(t, worker.resolveDependencies(ctx))
	resolved := dispatchAssignments(t, s)
	if len(resolved) != 2 {
		t.Fatal(resolved)
	}
	var consumer evolution.GenerationAssignment
	for _, a := range resolved {
		if a.BindingID == "lessons" {
			consumer = a
		}
	}
	if consumer.RunID != planned.RunID || consumer.BatchID != planned.BatchID || consumer.Pins.Base != planned.Pins.Base || len(consumer.Pins.Dependencies) != 1 || consumer.Pins.Dependencies[0].RunID != frozen[0].RunID {
		t.Fatal("consumer identity changed", consumer)
	}
	if _, err := s.AuthorizeGeneration(ctx, consumer); err != nil {
		t.Fatal("resolved consumer denied", err)
	}
	// A new attempt/batch/artifact or producer binding cannot substitute for the pins.
	for _, mutate := range []func(*evolution.GenerationAssignment){
		func(a *evolution.GenerationAssignment) { a.BatchID = "foreign-batch" },
		func(a *evolution.GenerationAssignment) { a.Pins.Dependencies[0].Fence++ },
		func(a *evolution.GenerationAssignment) { a.Pins.Dependencies[0].BindingID = "other" },
		func(a *evolution.GenerationAssignment) {
			a.Pins.Dependencies[0].Artifact.Digest = evolution.Digest(nil)
		},
	} {
		raw, _ := json.Marshal(consumer)
		var changed evolution.GenerationAssignment
		must(t, json.Unmarshal(raw, &changed))
		changed.RunID = "forged"
		mutate(&changed)
		if _, err := s.AuthorizeGeneration(ctx, changed); err == nil {
			t.Fatal("substituted dependency accepted", changed)
		}
	}
	worker.dependencyCursor = ""
	must(t, worker.resolveDependencies(ctx))
	if !reflect.DeepEqual(resolved, dispatchAssignments(t, s)) {
		t.Fatal("dependency replay mutated assignment")
	}
	completeDependency(t, s, consumer, "completed")
	parentFrontier, err := s.CorpusFrontier(ctx, token, "fixture-users", frozen[0].BindingID, source.ID)
	must(t, err)
	childFrontier, err := s.CorpusFrontier(ctx, token, "fixture-users", consumer.BindingID, source.ID)
	must(t, err)
	if childFrontier.Through != parentFrontier.Through || childFrontier.Through == 0 || childFrontier.RunID != consumer.RunID {
		t.Fatal("consumer lost source lineage", parentFrontier, childFrontier)
	}
	// The consumer does not directly pin the parent's composed input, so this
	// revocation specifically exercises recursive current producer authority.
	composed := frozen[0].Pins.InputPorts["corpus"]
	_, err = s.SetGenerationInputGrant(ctx, token, "fixture-users", GenerationInputGrantInput{RequestID: "revoke-parent-input", ExpectedRevision: 1, Artifact: composed, SourceIDs: []string{source.ID}, Enabled: false})
	must(t, err)
	if _, err = s.AuthorizeGeneration(ctx, consumer); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked ancestor input authorized", err)
	}
	_, err = s.SetGenerationInputGrant(ctx, token, "fixture-users", GenerationInputGrantInput{RequestID: "restore-parent-input", ExpectedRevision: 2, Artifact: composed, SourceIDs: []string{source.ID}, Enabled: true})
	must(t, err)
	// Current producer authority remains required when a consumer already exists.
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "deny-run", AccountID: scope.Actor, Action: "run.start", Effect: "deny"})
	must(t, err)
	if _, err = s.AuthorizeGeneration(ctx, consumer); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked dependency authority accepted", err)
	}
}
func TestGenerationDependencyFailureSkipsTransitiveConsumers(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	activateCorpusCollector(t, s, token, source, 1, true, func(c *evolution.Configuration) {
		dependencyConfig(c)
		grandchild := c.Bindings[0]
		grandchild.ID = "report"
		grandchild.Output.Prefix = "report"
		grandchild.DependsOn = []string{"lessons"}
		grandchild.Inputs = map[string]evolution.BindingInput{"evidence": {Dependency: &evolution.BindingDependency{BindingID: "lessons", OutputPort: "value"}}}
		c.Bindings = append([]evolution.Binding{grandchild}, c.Bindings...)
	})
	finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	remote := &dependencyTransport{store: s}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, EvolutionSpace: remote, Issuer: "https://app"}
	must(t, worker.prepare(ctx))
	must(t, worker.dispatch(ctx))
	root := dispatchAssignments(t, s)[0]
	completeDependency(t, s, root, "failed")
	for i := 0; i < 6; i++ {
		must(t, worker.resolveDependencies(ctx))
	}
	var skipped int
	must(t, s.db.QueryRow("SELECT count(*) FROM generation_preparations WHERE state='skipped' AND last_error='dependency_failed'").Scan(&skipped))
	if skipped != 2 || remote.reads != 0 || len(dispatchAssignments(t, s)) != 1 {
		t.Fatal("failed dependency published alone", skipped, remote.reads)
	}
	for _, item := range triggerItems(t, s, token) {
		if item.BindingID != "skills" && item.DispatchState != "skipped" {
			t.Fatal("skip not visible", item)
		}
	}
}
