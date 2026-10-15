package runner

import (
	"aster/evaluation-space/internal/httpapi"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

type centralGeneration struct {
	assignment  evolution.GenerationAssignment
	grant       evolution.GenerationGrant
	state       string
	fence       int64
	spent       projections.RunUsage
	settlements int
	deny        bool
}

func (c *centralGeneration) ResolveGeneration(_ context.Context, scope evolution.GenerationGrantScope) (evolution.GenerationAssignment, error) {
	if scope.Grant != c.grant || scope.Fence != c.fence || c.deny && scope.Action != "cleanup" {
		return c.assignment, errors.New("denied")
	}
	if scope.Action == "execute" && c.state != "admitted" && c.state != "running" {
		return c.assignment, errors.New("wrong state")
	}
	return c.assignment, nil
}
func (c *centralGeneration) ObserveGeneration(_ context.Context, value evolution.GenerationObservation) error {
	if value.Grant != c.grant || value.Fence < c.fence {
		return errors.New("stale observation")
	}
	c.state = value.State
	c.fence = value.Fence
	return nil
}
func (c *centralGeneration) SettleGeneration(_ context.Context, grant evolution.GenerationGrant, fence, tokens, cost int64) error {
	if grant != c.grant || fence != c.fence {
		return errors.New("stale settlement")
	}
	c.settlements++
	c.spent = projections.RunUsage{Tokens: tokens, CostMicros: cost}
	return nil
}
func TestCentralNativeAuthorityBindsClaimAndTerminalSettlement(t *testing.T) {
	store, _, run, _, _ := nativeFixture(t)
	ctx := context.Background()
	assignment := evolution.GenerationAssignment{RunID: run.ID, GroupID: run.GroupID, BindingID: run.BindingID, OccurrenceID: "occurrence", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Pins: run.Pins, Budget: run.Budget}

	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "grant", RunID: run.ID, GroupID: run.GroupID, AssignmentDigest: digest}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	forged := grant
	forged.AssignmentDigest = evolution.Digest([]byte("other"))
	if err = store.RetainGenerationGrant(ctx, assignment, forged); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("grant rebound", err)
	}
	central := &centralGeneration{assignment: assignment, grant: grant, state: "admitted"}
	authority := CentralAuthority{Store: store, Central: central}
	if err = authority.AuthorizeRun(ctx, run, false); err != nil {
		t.Fatal(err)
	}
	// Exercise the claim/cleanup/settlement boundary with retained exact grant.
	lease, err := store.ClaimRun(ctx, run.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	run.State = "cancelling"
	run.Fence = lease.Fence
	if err = authority.AuthorizeClaim(ctx, lease, run, true); err != nil {
		t.Fatal(err)
	}
	if central.fence != lease.Fence || central.state != "cancelling" {
		t.Fatal("claim not observed")
	}
	if err = store.SettleRun(ctx, lease, "cancelled"); err != nil {
		t.Fatal(err)
	}
	run.State = "cancelled"
	if err = authority.ReconcileTerminal(ctx, run); err != nil {
		t.Fatal(err)
	}
	if central.state != "cancelled" || central.settlements != 1 || central.spent != (projections.RunUsage{}) {
		t.Fatal(central)
	}
	pending, err := store.UnsettledGenerations(ctx, "", 100)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	_, _, err = store.GenerationGrant(ctx, "other", run.ID)
	if !errors.Is(err, projections.ErrNotFound) {
		t.Fatal("grant crossed group", err)
	}
}

func TestCentralNativeWorkerCompletesAndSettlesExactGrant(t *testing.T) {
	store, _, run, executor, provider := nativeFixture(t)
	ctx := context.Background()
	assignment := evolution.GenerationAssignment{RunID: run.ID, GroupID: run.GroupID, BindingID: run.BindingID, OccurrenceID: "occurrence", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Pins: run.Pins, Budget: run.Budget}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "central-output-grant", RunID: run.ID, GroupID: run.GroupID, AssignmentDigest: digest}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	central := &centralGeneration{assignment: assignment, grant: grant, state: "admitted"}
	authority := CentralAuthority{Store: store, Central: central}
	executor.Authority = authority
	worker := Worker{Store: store, ID: "worker", Authority: authority, Executor: executor}
	if err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil || completed.State != "completed" {
		t.Fatal(completed, err)
	}
	if central.state != "running" || central.fence != completed.Fence || provider.calls != 1 {
		t.Fatal("claim authority not synchronized")
	}
	if err = authority.ReconcileTerminal(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if central.state != "completed" || central.spent.Tokens != 4 || central.spent.CostMicros != 7 {
		t.Fatal("usage not reconciled", central)
	}
	checkpoint, err := store.LatestRunCheckpoint(ctx, run.GroupID, run.ID)
	if err != nil || checkpoint.Artifact.GrantID != grant.ID {
		t.Fatal("output grant not bound", checkpoint, err)
	}
	scope := evolution.GenerationOutputScope{GroupID: run.GroupID, RunID: run.ID, Port: "value", Fence: completed.Fence}
	output, err := store.GenerationOutput(ctx, scope)
	if err != nil || output.Artifact.GrantID != grant.ID || output.Artifact.SchemaDigest != run.Pins.InputPorts["value"].SchemaDigest {
		t.Fatal("typed output absent", output, err)
	}
	raw, err := store.Artifact(ctx, output.Artifact)
	if err != nil || string(raw) != `"MODEL OUTPUT"` {
		t.Fatal("typed output value", string(raw), err)
	}
	replay, err := store.GenerationOutput(ctx, scope)
	if err != nil || replay != output {
		t.Fatal("typed output replay changed", replay, err)
	}
	api, err := httpapi.NewGenerationAPI(store, central, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	call := func(scope evolution.GenerationOutputScope, user string) int {
		raw, _ := json.Marshal(scope)
		request := httptest.NewRequest("POST", "/evaluation-space/internal/v1/generation/output", bytes.NewReader(raw))
		request.SetBasicAuth(user, strings.Repeat("s", 32))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		return response.Code
	}
	if status := call(scope, "central"); status != 200 {
		t.Fatal("output route", status)
	}
	if status := call(scope, "producer"); status != 403 {
		t.Fatal("producer borrowed output read", status)
	}
	wrong := scope
	wrong.Port = "unknown"
	if status := call(wrong, "central"); status != 404 {
		t.Fatal("undeclared output port", status)
	}
	wrong = scope
	wrong.Fence++
	if status := call(wrong, "central"); status == 200 {
		t.Fatal("wrong output fence accepted")
	}
	central.deny = true
	if status := call(scope, "central"); status != 403 {
		t.Fatal("revoked output read", status)
	}
	central.deny = false
	// A foreign artifact with an otherwise plausible output grant is still denied.
	artifact := checkpoint.Artifact
	artifact.GroupID = "other"
	if err = authority.AuthorizeArtifact(ctx, completed, artifact); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("foreign artifact authorized", err)
	}
}

func TestCentralAuthorityRejectsSubstitutedRunPinsAndOutputGrant(t *testing.T) {
	store, _, run, _, _ := nativeFixture(t)
	ctx := context.Background()
	assignment := evolution.GenerationAssignment{RunID: run.ID, GroupID: run.GroupID, BindingID: run.BindingID, OccurrenceID: "occurrence", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Pins: run.Pins, Budget: run.Budget}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "grant", RunID: run.ID, GroupID: run.GroupID, AssignmentDigest: digest}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	authority := CentralAuthority{Store: store, Central: &centralGeneration{assignment: assignment, grant: grant, state: "admitted"}}
	changed := run
	changed.Pins.Workflow.Revision++
	if err = authority.AuthorizeRun(ctx, changed, false); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("changed workflow accepted", err)
	}
	artifact := run.Pins.Inputs[0]
	artifact.ID = "derived"
	artifact.GrantID = "foreign-output-grant"
	if err = authority.AuthorizeArtifact(ctx, run, artifact); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("foreign output grant", err)
	}
}

func (c *centralGeneration) AuthorizeGenerationInput(context.Context, evolution.ArtifactRef) error {
	if c.deny {
		return errors.New("denied")
	}
	return nil
}
func (c *centralGeneration) AdmitGeneration(_ context.Context, a evolution.GenerationAssignment) (evolution.GenerationGrant, error) {
	digest, err := a.Digest()
	if err != nil || digest != c.grant.AssignmentDigest {
		return evolution.GenerationGrant{}, errors.New("denied")
	}
	return c.grant, nil
}
