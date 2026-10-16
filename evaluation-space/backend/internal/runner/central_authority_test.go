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
	if scope.Grant != c.grant || scope.Action != "read" && scope.Fence != c.fence || c.deny && scope.Action != "cleanup" {
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
	changed := run
	changed.DraftTest = &evolution.GenerationDraftTest{Source: grant, Draft: run.Pins.Workflow, Kind: "workflow"}
	if err = authority.AuthorizeRun(ctx, changed, false); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("run changed test classification", err)
	}
	changedAssignment := assignment
	sourceGrant := grant
	sourceGrant.RunID = "source"
	changedAssignment.DraftTest = &evolution.GenerationDraftTest{Source: sourceGrant, Draft: run.Pins.Workflow, Kind: "workflow"}
	changedDigest, err := changedAssignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changedGrant := grant
	changedGrant.AssignmentDigest = changedDigest
	if err = store.RetainGenerationGrant(ctx, changedAssignment, changedGrant); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("test grant attached to production run", err)
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
	if err != nil || digest != c.grant.AssignmentDigest || c.deny {
		return evolution.GenerationGrant{}, errors.New("denied")
	}
	return c.grant, nil
}

// A real CentralAuthority must recover the admission intent before executing a
// retry. The service fake models an admission committed before its reply is lost.
type retryAdmissionCentral struct {
	centralGeneration
	admissions int
	loseReply  bool
}

func (c *retryAdmissionCentral) AdmitGeneration(ctx context.Context, input evolution.GenerationAssignment) (evolution.GenerationGrant, error) {
	c.admissions++
	grant, err := c.centralGeneration.AdmitGeneration(ctx, input)
	if err != nil {
		return grant, err
	}
	if c.loseReply {
		c.loseReply = false
		return evolution.GenerationGrant{}, errors.New("lost admission reply")
	}
	return grant, nil
}
func TestCentralRetryRecoversAdmissionBeforeExecutionOrCancellation(t *testing.T) {
	for _, mode := range []string{"execute", "revoked", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, path, source, executor, provider := nativeFixture(t)
			ctx := context.Background()
			worker := Worker{Store: store, ID: "source-worker", Authority: executor.Authority, Executor: executor}
			if err := worker.Step(ctx); err != nil {
				t.Fatal(err)
			}
			retry, err := store.RetryRun(ctx, projections.RunRetry{GroupID: source.GroupID, RequestID: "retry", SourceRunID: source.ID, NewRunID: "retry-run"}, evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"})
			if err != nil {
				t.Fatal(err)
			}
			assignment, err := store.PendingGenerationAssignment(ctx, source.GroupID, retry.ID)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := assignment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			grant := evolution.GenerationGrant{ID: "retry-grant", RunID: retry.ID, GroupID: retry.GroupID, AssignmentDigest: digest}
			central := &retryAdmissionCentral{centralGeneration: centralGeneration{assignment: assignment, grant: grant, state: "admitted"}, loseReply: true}
			authority := CentralAuthority{Store: store, Central: central}
			if err = authority.AuthorizeRun(ctx, retry, false); err == nil {
				t.Fatal("lost reply succeeded")
			}
			if _, _, err = store.GenerationGrant(ctx, retry.GroupID, retry.ID); !errors.Is(err, projections.ErrNotFound) {
				t.Fatal("lost reply retained authority", err)
			}
			if provider.calls != 1 {
				t.Fatal("executed before admission")
			}
			// Reopen durable state: no in-memory request/receipt is needed to recover.
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = projections.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			authority.Store = store
			executor.Registry.Store = store
			provider.store = store
			executor.Authority = authority
			provider.outcome = nil
			central.deny = mode == "revoked"
			if mode == "cancel" {
				_, err = store.ControlRun(ctx, projections.RunControl{RequestID: "cancel-retry", RunID: retry.ID, Action: "cancel", ExpectedFence: 0})
				if err != nil {
					t.Fatal(err)
				}
				pending, err := store.UnsettledGenerations(ctx, "", 100)
				if err != nil || len(pending) != 1 || pending[0].RunID != retry.ID {
					t.Fatal("cancelled pending admission lost", pending, err)
				}
				retry, err = store.Run(ctx, retry.GroupID, retry.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err = authority.ReconcileTerminal(ctx, retry); err != nil {
					t.Fatal(err)
				}
				if central.state != "cancelled" || central.spent != (projections.RunUsage{}) || provider.calls != 1 {
					t.Fatal("cancelled admission executed or retained budget", central)
				}
			} else {
				worker = Worker{Store: store, ID: "retry-worker", Authority: authority, Executor: executor}
				err = worker.Step(ctx)
				if mode == "revoked" {
					if err == nil || provider.calls != 1 {
						t.Fatal("revoked receipt permitted execution", err, provider.calls)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					retry, err = store.Run(ctx, retry.GroupID, retry.ID)
					if err != nil || retry.State != "completed" || provider.calls != 2 {
						t.Fatal(retry, err, provider.calls)
					}
					if err = authority.ReconcileTerminal(ctx, retry); err != nil {
						t.Fatal(err)
					}
					if central.spent.Tokens != 4 || central.spent.CostMicros != 7 {
						t.Fatal("retry usage missing", central.spent)
					}
				}
			}
			if central.admissions != 2 {
				t.Fatal("admission not replayed exactly", central.admissions)
			}
			_, retained, err := store.GenerationGrant(ctx, retry.GroupID, retry.ID)
			if mode == "revoked" {
				if !errors.Is(err, projections.ErrNotFound) {
					t.Fatal("revoked pending admission retained a grant", err)
				}
			} else if err != nil || retained != grant {
				t.Fatal("retry grant missing", retained, err)
			}
			original, err := store.Run(ctx, source.GroupID, source.ID)
			if err != nil || original.State != "completed" || original.RetryOf != "" {
				t.Fatal("source mutated", original, err)
			}
		})
	}
}

func (c *centralGeneration) CancelGenerationAdmission(ctx context.Context, input evolution.GenerationAdmissionCancellation) (evolution.GenerationGrant, error) {
	digest, err := input.Assignment.Digest()
	if err != nil || digest != c.grant.AssignmentDigest {
		return evolution.GenerationGrant{}, errors.New("wrong cancellation assignment")
	}
	return c.grant, nil
}
func (c *retryAdmissionCentral) CancelGenerationAdmission(ctx context.Context, input evolution.GenerationAdmissionCancellation) (evolution.GenerationGrant, error) {
	c.admissions++
	return c.centralGeneration.CancelGenerationAdmission(ctx, input)
}

type pauseObservationCentral struct {
	centralGeneration
	losePause bool
}

func (c *pauseObservationCentral) ObserveGeneration(ctx context.Context, input evolution.GenerationObservation) error {
	err := c.centralGeneration.ObserveGeneration(ctx, input)
	if err == nil && input.State == "paused" && c.losePause {
		c.losePause = false
		return errors.New("lost pause observation reply")
	}
	return err
}
func TestCentralNativeCooperativePauseDrainsNodeAndResumesWithoutReplay(t *testing.T) {
	for _, losePause := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged", true: "lost-observation"}[losePause], func(t *testing.T) {
			store, _, run, executor, provider := nativeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			assignment := evolution.GenerationAssignment{RunID: run.ID, GroupID: run.GroupID, BindingID: run.BindingID, OccurrenceID: "occurrence", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Pins: run.Pins, Budget: run.Budget}
			digest, err := assignment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			grant := evolution.GenerationGrant{ID: "pause-grant", RunID: run.ID, GroupID: run.GroupID, AssignmentDigest: digest}
			if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
				t.Fatal(err)
			}
			central := &pauseObservationCentral{centralGeneration: centralGeneration{assignment: assignment, grant: grant, state: "admitted"}, losePause: losePause}
			authority := CentralAuthority{Store: store, Central: central}
			executor.Authority = authority
			started, release := make(chan struct{}), make(chan struct{})
			provider.beforeSubmit = func(ctx context.Context, _ projections.ProviderCall) error {
				close(started)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			worker := Worker{Store: store, ID: "worker", Authority: authority, Executor: executor}
			done := make(chan error, 1)
			go func() { done <- worker.Step(ctx) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			active, err := store.Run(ctx, run.GroupID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			requested, err := store.ControlRun(ctx, projections.RunControl{RequestID: "pause", RunID: run.ID, ExpectedFence: active.Fence, Action: "pause"})
			if err != nil || requested.State != "pause_requested" || requested.Fence != active.Fence {
				t.Fatal("pause fenced admitted work", requested, err)
			}
			if err = store.AcknowledgeRunPause(ctx, projections.RunLease{RunID: run.ID, WorkerID: "worker", Fence: active.Fence}); !errors.Is(err, projections.ErrConflict) {
				t.Fatal("uncertain provider work acknowledged paused", err)
			}
			close(release)
			err = <-done
			if (err != nil) != losePause {
				t.Fatal("pause worker result", err)
			}
			paused, err := store.Run(ctx, run.GroupID, run.ID)
			if err != nil || paused.State != "paused" || paused.Fence <= active.Fence {
				t.Fatal("pause not durable", paused, err)
			}
			attempts, err := store.WorkflowAttempts(ctx, run.GroupID, run.ID, "", 100)
			if err != nil || len(attempts) != 1 || attempts[0].Call.Node.ID != "agent" || attempts[0].State != "succeeded" || provider.calls != 1 {
				t.Fatal("pause lost output or scheduled downstream work", attempts, provider.calls, err)
			}
			checkpoint, err := store.LatestRunCheckpoint(ctx, run.GroupID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.ReadWorkflowCheckpoint(ctx, run.GroupID, run.ID, checkpoint.ID); err != nil {
				t.Fatal("pause checkpoint missing", err)
			}
			pending, err := store.PendingGenerationPauses(ctx, "", 10)
			if err != nil || (len(pending) == 1) != losePause {
				t.Fatal("pause observation recovery missing", pending, err)
			}
			if err = authority.ReconcilePause(ctx, paused); err != nil {
				t.Fatal(err)
			}
			pending, err = store.PendingGenerationPauses(ctx, "", 10)
			if err != nil || len(pending) != 0 || central.state != "paused" || central.fence != paused.Fence {
				t.Fatal("pause not observed exactly", pending, central, err)
			}
			_, err = store.ControlRun(ctx, projections.RunControl{RequestID: "resume", RunID: run.ID, ExpectedFence: paused.Fence, Action: "resume"})
			if err != nil {
				t.Fatal(err)
			}
			worker = Worker{Store: store, ID: "resumed", Authority: authority, Executor: executor}
			if err = worker.Step(ctx); err != nil {
				t.Fatal(err)
			}
			completed, err := store.Run(ctx, run.GroupID, run.ID)
			if err != nil || completed.State != "completed" || provider.calls != 1 {
				t.Fatal("resume repeated model effect", completed, provider.calls, err)
			}
			frozen, err := store.CheckpointWorkflowAttempts(ctx, run.GroupID, run.ID, checkpoint.ID, "", 100)
			if err != nil || len(frozen) != 1 {
				t.Fatal("resume changed paused checkpoint", frozen, err)
			}
			if err = authority.ReconcileTerminal(ctx, completed); err != nil {
				t.Fatal(err)
			}
			if central.spent.Tokens != 4 || central.spent.CostMicros != 7 {
				t.Fatal("pause/resume charged twice", central.spent)
			}
		})
	}
}

func TestCentralPauseBetweenNodesRecoversObservationAfterRestart(t *testing.T) {
	store, path, run, _, _ := nativeFixture(t)
	ctx := context.Background()
	assignment := evolution.GenerationAssignment{RunID: run.ID, GroupID: run.GroupID, BindingID: run.BindingID, OccurrenceID: "occurrence", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Pins: run.Pins, Budget: run.Budget}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "pause-grant", RunID: run.ID, GroupID: run.GroupID, AssignmentDigest: digest}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	central := &centralGeneration{assignment: assignment, grant: grant, state: "admitted"}
	lease, err := store.ClaimRun(ctx, run.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authority := CentralAuthority{Store: store, Central: central}
	if err = authority.AuthorizeClaim(ctx, lease, run, false); err != nil {
		t.Fatal(err)
	}
	paused, err := store.ControlRun(ctx, projections.RunControl{RequestID: "between-nodes", RunID: run.ID, Action: "pause", ExpectedFence: lease.Fence})
	if err != nil || paused.State != "paused" {
		t.Fatal(paused, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authority.Store = store
	pending, err := store.PendingGenerationPauses(ctx, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatal("control observation lost at restart", pending, err)
	}
	current, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	central.deny = true // Recording an already-durable pause is cleanup, not execution.
	if err = authority.ReconcilePause(ctx, current); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingGenerationPauses(ctx, "", 10)
	if err != nil || len(pending) != 0 || central.state != "paused" || central.fence != current.Fence {
		t.Fatal("pause not delivered", pending, central, err)
	}
}
