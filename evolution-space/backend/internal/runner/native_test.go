package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/workflow"
)

type nativeAuthority struct{ deny bool }

func (a *nativeAuthority) AuthorizeRun(context.Context, evolution.Run, bool) error {
	if a.deny {
		return errors.New("revoked")
	}
	return nil
}
func (a *nativeAuthority) AuthorizeArtifact(context.Context, evolution.Run, evolution.ArtifactRef) error {
	if a.deny {
		return errors.New("revoked")
	}
	return nil
}
func (a *nativeAuthority) OutputGrant(context.Context, evolution.Run) (string, error) {
	return "run-grant", nil
}

type nativeProvider struct {
	beforeSubmit func(context.Context, projections.ProviderCall) error
	store        *projections.Store
	calls        int
	lost         bool
	allowLookup  bool
	outcome      *projections.ProviderOutcome
	prompt       string
}

func (p *nativeProvider) Lookup(context.Context, projections.ProviderCall) (projections.ProviderOutcome, error) {
	if p.outcome != nil {
		if p.allowLookup {
			return *p.outcome, nil
		}
		return projections.ProviderOutcome{}, errors.New("uncertain lookup")
	}
	return projections.ProviderOutcome{}, projections.ErrProviderCallNotFound
}
func (p *nativeProvider) Submit(ctx context.Context, call projections.ProviderCall, raw []byte, _ projections.RunUsage) (projections.ProviderOutcome, error) {
	p.calls++
	if p.beforeSubmit != nil {
		if err := p.beforeSubmit(ctx, call); err != nil {
			return projections.ProviderOutcome{}, err
		}
	}
	input, err := evolution.Decode[workflow.Call](bytes.NewReader(raw))
	if err != nil {
		return projections.ProviderOutcome{}, err
	}
	p.prompt = input.Prompt
	out, _ := json.Marshal(workflow.Values{"value": json.RawMessage(`"model output"`)})
	artifact := evolution.ArtifactRef{ID: "output-" + call.RunID + "-" + call.ID, GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(out), Size: int64(len(out)), MediaType: "application/json", GrantID: call.Request.GrantID, PolicyRevision: 1}
	if err = p.store.SealArtifact(ctx, artifact, bytes.NewReader(out)); err != nil {
		return projections.ProviderOutcome{}, err
	}
	result := projections.ProviderOutcome{CallID: call.ID, ProviderRequestID: "request", State: "completed", Usage: projections.RunUsage{Tokens: 4, CostMicros: 7}, Output: &artifact}
	p.outcome = &result
	if p.lost {
		return projections.ProviderOutcome{}, errors.New("lost response")
	}
	return result, nil
}
func (p *nativeProvider) Cancel(context.Context, projections.ProviderCall) (projections.ProviderOutcome, error) {
	if p.outcome != nil {
		return *p.outcome, nil
	}
	return projections.ProviderOutcome{}, errors.New("unknown cancellation")
}
func nativeFixture(t *testing.T, configure ...func(*evolution.Workflow)) (*projections.Store, string, evolution.Run, *NativeExecutor, *nativeProvider) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	schema := json.RawMessage(`{"type":"string"}`)
	digest := evolution.Digest(schema)
	ref := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	text := "Read {{value}}"
	graph := evolution.Workflow{SchemaVersion: 1, ID: "native", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: map[string]string{"value": digest}, Outputs: map[string]string{"value": digest}, Nodes: []evolution.Node{{ID: "agent", Kind: "agent", InputPorts: map[string]string{"value": digest}, OutputPorts: map[string]string{"value": digest}, Prompt: &evolution.Prompt{Text: text, Variables: []string{"value"}, Digest: evolution.Digest([]byte(text))}, ModelRef: &ref, MaxAttempts: 1, TimeoutSeconds: 10}, {ID: "build", Kind: "transform", InputPorts: map[string]string{"value": digest}, OutputPorts: map[string]string{"value": digest}, ImplementationRef: &ref, MaxAttempts: 1, TimeoutSeconds: 10}}, Edges: []evolution.Edge{{From: "$input.value", To: "agent.value"}, {From: "agent.value", To: "build.value"}, {From: "build.value", To: "$output.value"}}, Limits: evolution.Limits{MaxParallelNodes: 2, MaxTotalAttempts: 10, TimeoutSeconds: 60}}
	for _, apply := range configure {
		apply(&graph)
	}
	raw, err := graph.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	workflowRef := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = store.PublishWorkflow(ctx, workflowRef, raw); err != nil {
		t.Fatal(err)
	}
	input := []byte(`"corpus"`)
	artifact := evolution.ArtifactRef{ID: "corpus", GroupID: "group", SchemaDigest: digest, Digest: evolution.Digest(input), Size: int64(len(input)), MediaType: "application/json", GrantID: "source-grant", PolicyRevision: 1}
	if err = store.SealArtifact(ctx, artifact, bytes.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "native-service", Revision: 1}, Kind: "evolution", ConnectionID: "native", RunnerDigest: evolution.Digest([]byte("runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	serviceRaw, _ := json.Marshal(service)
	if err = store.PublishDefinition(ctx, "service", serviceRaw); err != nil {
		t.Fatal(err)
	}
	run := evolution.Run{ID: "run", GroupID: "group", BindingID: "binding", State: "queued", CreatedAt: time.Now().UTC(), Budget: evolution.Budget{MaxAttempts: 10, MaxTokens: 100, MaxCostMicros: 100, MaxArtifactBytes: 1 << 20, TimeoutSeconds: 60}, Pins: evolution.RunPins{Service: service.Ref, Workflow: workflowRef, Inputs: []evolution.ArtifactRef{artifact}, InputPorts: map[string]evolution.ArtifactRef{"value": artifact}, SourcePolicyRevision: 1, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("base"))}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "lessons", Profile: "authored-v1", ReservationGeneration: 1}}}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(run)
	_, err = db.Exec(`INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	provider := &nativeProvider{store: store}
	registry := &NativeRegistry{Store: store, Schemas: map[string]json.RawMessage{digest: schema}, Models: map[evolution.Revision]Model{ref: {Provider: provider, Ceiling: projections.RunUsage{Tokens: 10, CostMicros: 20}}}, Transforms: map[evolution.Revision]Transform{ref: func(_ context.Context, call workflow.Call) (workflow.Values, error) {
		var text string
		_ = json.Unmarshal(call.Inputs["value"], &text)
		raw, _ := json.Marshal(strings.ToUpper(text))
		return workflow.Values{"value": raw}, nil
	}}}
	return store, path, run, &NativeExecutor{Registry: registry, Authority: &nativeAuthority{}}, provider
}
func TestNativeRunExecutesPinnedGraphAndRecordsBudgetedResult(t *testing.T) {
	store, _, run, executor, provider := nativeFixture(t)
	ctx := context.Background()
	worker := Worker{Store: store, ID: "worker", Authority: executor.Authority, Executor: executor}
	if err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil || current.State != "completed" || provider.calls != 1 || provider.prompt != "Read corpus" {
		t.Fatal(current, err, provider.calls, provider.prompt)
	}
	checkpoint, err := store.LatestRunCheckpoint(ctx, run.GroupID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Artifact(ctx, checkpoint.Artifact)
	if err != nil || string(raw) != `{"value":"MODEL OUTPUT"}` {
		t.Fatal(string(raw), err)
	}
	budget, err := store.RunBudget(ctx, run.GroupID, run.ID)
	if err != nil || budget.Spent.Tokens != 4 || budget.Spent.CostMicros != 7 || budget.Held.Tokens != 0 {
		t.Fatal(budget, err)
	}
	attempts, err := store.WorkflowAttempts(ctx, run.GroupID, run.ID, "", 100)
	if err != nil || len(attempts) != 2 {
		t.Fatal(attempts, err)
	}
	events, err := store.RunEvents(ctx, run.GroupID, run.ID, 0, 100)
	if err != nil || len(events.Events) != 4 {
		t.Fatal(events, err)
	}
	if err = worker.Step(ctx); err != nil || provider.calls != 1 {
		t.Fatal("terminal model rerun", err)
	}
	debugContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	debug, err := store.RunDebugger(debugContext, run.GroupID, run.ID, "", 100)
	if err != nil || len(debug.Observations) != 1 || debug.Observations[0].Outcome.Usage.Tokens != 4 {
		t.Fatal("native provider evidence unavailable", debug, err)
	}
}

func TestNativeTerminalRetryCreatesIndependentRunAndExecutes(t *testing.T) {
	store, _, source, executor, provider := nativeFixture(t)
	ctx := context.Background()
	worker := Worker{Store: store, ID: "source-worker", Authority: executor.Authority, Executor: executor}
	if err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := store.Run(ctx, source.GroupID, source.ID)
	if err != nil || before.State != "completed" {
		t.Fatal(before, err)
	}
	created, err := store.RetryRun(ctx, projections.RunRetry{GroupID: source.GroupID, RequestID: "native-retry", SourceRunID: source.ID, NewRunID: "native-retry-run"}, evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"})
	if err != nil || created.State != "queued" || created.RetryOf != source.ID {
		t.Fatal(created, err)
	}
	if !bytes.Equal(mustJSON(t, created.Pins), mustJSON(t, before.Pins)) {
		t.Fatal("retry changed frozen pins")
	}
	provider.outcome = nil
	retryWorker := Worker{Store: store, ID: "retry-worker", Authority: executor.Authority, Executor: executor}
	if err = retryWorker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := store.Run(ctx, source.GroupID, created.ID)
	if err != nil || after.State != "completed" || provider.calls != 2 {
		t.Fatal(after, err, provider.calls)
	}
	unchanged, err := store.Run(ctx, source.GroupID, source.ID)
	if err != nil || unchanged.State != "completed" || unchanged.RetryOf != "" {
		t.Fatal(unchanged, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestNativeRecoveryUsesOriginalModelCallAndCheckpoint(t *testing.T) {
	store, path, run, executor, provider := nativeFixture(t)
	ctx := context.Background()
	provider.lost = true
	worker := Worker{Store: store, ID: "first", Authority: executor.Authority, Executor: executor}
	if err := worker.Step(ctx); err == nil {
		t.Fatal("uncertain response hidden")
	}
	if provider.calls != 1 {
		t.Fatal(provider.calls)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE runs SET lease_until=0 WHERE id='run'")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider.store = store
	provider.allowLookup = true
	executor.Registry.Store = store
	worker = Worker{Store: store, ID: "replacement", Authority: executor.Authority, Executor: executor}
	if err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil || current.State != "completed" || provider.calls != 1 {
		t.Fatal(current, err, provider.calls)
	}
}
func TestNativeCancellationReconcilesWithoutNewGeneration(t *testing.T) {
	store, _, run, executor, provider := nativeFixture(t)
	ctx := context.Background()
	provider.lost = true
	worker := Worker{Store: store, ID: "first", Authority: executor.Authority, Executor: executor}
	if err := worker.Step(ctx); err == nil {
		t.Fatal("uncertain response hidden")
	}
	current, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := store.ControlRunScoped(ctx, run.GroupID, projections.RunControl{GroupID: run.GroupID, RequestID: "cancel", RunID: run.ID, ExpectedFence: current.Fence, Action: "cancel"})
	if err != nil || canceled.State != "cancelling" {
		t.Fatal(canceled, err)
	}
	worker = Worker{Store: store, ID: "cleanup", Authority: executor.Authority, Executor: executor}
	if err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = store.Run(ctx, run.GroupID, run.ID)
	if err != nil || current.State != "cancelled" || provider.calls != 1 {
		t.Fatal(current, err, provider.calls)
	}
	if _, err = store.LatestRunCheckpoint(ctx, run.GroupID, run.ID); !errors.Is(err, projections.ErrNotFound) {
		t.Fatal("canceled run produced result", err)
	}
}

func TestNativeRunRejectsRevokedInputsBeforeModelWork(t *testing.T) {
	store, _, _, executor, provider := nativeFixture(t)
	ctx := context.Background()
	executor.Authority = &nativeAuthority{deny: true}
	worker := Worker{Store: store, ID: "worker", Authority: &nativeAuthority{}, Executor: executor}
	if err := worker.Step(ctx); err == nil || provider.calls != 0 {
		t.Fatal("revoked inputs reached model", err, provider.calls)
	}
}

func TestPausedCheckpointExperimentReusesNativeOutputThroughCentralAdmission(t *testing.T) {
	store, _, run, executor, provider := nativeFixture(t)
	ctx := context.Background()
	requester := evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}
	assignment := evolution.GenerationAssignment{RunID: run.ID, GroupID: run.GroupID, BindingID: run.BindingID, OccurrenceID: "source", Requester: requester, Pins: run.Pins, Budget: run.Budget}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "run-grant", RunID: run.ID, GroupID: run.GroupID, AssignmentDigest: digest}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	provider.beforeSubmit = func(ctx context.Context, _ projections.ProviderCall) error {
		source, err := store.Run(ctx, run.GroupID, run.ID)
		if err != nil {
			return err
		}
		_, err = store.ControlRun(ctx, projections.RunControl{RequestID: "pause-source", RunID: run.ID, ExpectedFence: source.Fence, Action: "pause"})
		return err
	}
	worker := Worker{Store: store, ID: "source-worker", Authority: executor.Authority, Executor: executor}
	if err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	source, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil || source.State != "paused" || provider.calls != 1 {
		t.Fatal(source, err, provider.calls)
	}
	checkpoint, err := store.LatestRunCheckpoint(ctx, run.GroupID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := store.Workflow(ctx, run.Pins.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	editedRef := evolution.Revision{ID: "edited-builder", Revision: 1, Digest: evolution.Digest([]byte("edited-builder"))}
	graph.Nodes[1].ImplementationRef = &editedRef
	executor.Registry.Transforms[editedRef] = func(_ context.Context, call workflow.Call) (workflow.Values, error) {
		var text string
		_ = json.Unmarshal(call.Inputs["value"], &text)
		raw, _ := json.Marshal("edited " + text)
		return workflow.Values{"value": raw}, nil
	}
	created, err := store.CreateRunExperiment(ctx, projections.RunExperimentRequest{ID: "experiment", GroupID: run.GroupID, SourceRunID: run.ID, CheckpointID: checkpoint.ID, StartNode: "build", Workflow: graph}, requester)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingGenerationAssignment(ctx, run.GroupID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err = pending.Digest()
	if err != nil {
		t.Fatal(err)
	}
	experimentGrant := evolution.GenerationGrant{ID: "experiment-grant", RunID: created.ID, GroupID: created.GroupID, AssignmentDigest: digest}
	central := &retryAdmissionCentral{centralGeneration: centralGeneration{assignment: pending, grant: experimentGrant, state: "admitted"}, loseReply: true}
	authority := CentralAuthority{Store: store, Central: central}
	executor.Authority = authority
	worker = Worker{Store: store, ID: "experiment-worker", Authority: authority, Executor: executor}
	if err = worker.Step(ctx); err == nil {
		t.Fatal("lost admission reply ignored")
	}
	worker = Worker{Store: store, ID: "recovered-worker", Authority: authority, Executor: executor}
	if err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := store.Run(ctx, run.GroupID, created.ID)
	if err != nil || current.State != "completed" || provider.calls != 1 {
		t.Fatal(current, err, provider.calls)
	}
	attempts, err := store.WorkflowAttempts(ctx, run.GroupID, created.ID, "", 100)
	if err != nil || len(attempts) != 2 {
		t.Fatal(attempts, err)
	}
	reused := 0
	for _, record := range attempts {
		if record.ReusedFrom != nil {
			reused++
			if record.Call.Node.ID != "agent" || record.ReusedFrom.RunID != run.ID || record.ReusedFrom.CheckpointID != checkpoint.ID {
				t.Fatal(record)
			}
		}
	}
	if reused != 1 {
		t.Fatal("explicit reuse provenance missing", attempts)
	}
	final, err := store.LatestRunCheckpoint(ctx, run.GroupID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Artifact(ctx, final.Artifact)
	if err != nil || string(raw) != `{"value":"edited model output"}` {
		t.Fatal(string(raw), err)
	}
	if err = authority.ReconcileTerminal(ctx, current); err != nil {
		t.Fatal(err)
	}
	if central.spent != (projections.RunUsage{}) {
		t.Fatal("reused output charged as fresh model work", central.spent)
	}
	retry, err := store.RetryRun(ctx, projections.RunRetry{RequestID: "retry-experiment", GroupID: run.GroupID, SourceRunID: created.ID, NewRunID: "retried-experiment"}, requester)
	if err != nil {
		t.Fatal(err)
	}
	retryAssignment, err := store.PendingGenerationAssignment(ctx, run.GroupID, retry.ID)
	if err != nil || retryAssignment.Experiment == nil || *retryAssignment.Experiment != *created.Experiment || retry.RetryOf != created.ID {
		t.Fatal("experiment retry lost admission lineage", retryAssignment, err)
	}

	original, err := store.WorkflowAttempts(ctx, run.GroupID, run.ID, "", 100)
	if err != nil || len(original) != 1 || original[0].ReusedFrom != nil {
		t.Fatal("source history changed", original, err)
	}
	source, err = store.Run(ctx, run.GroupID, run.ID)
	if err != nil || source.State != "paused" || source.Pins.Workflow != run.Pins.Workflow {
		t.Fatal(source, err)
	}
}
