package runner

import (
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestNativeDraftTestsExecuteFreshScopesAndRetainClassification(t *testing.T) {
	for _, kind := range []string{"workflow", "node", "subgraph"} {
		t.Run(kind, func(t *testing.T) {
			store, path, source, executor, provider := nativeFixture(t)
			ctx := context.Background()
			requester := evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}
			original := evolution.GenerationAssignment{RunID: source.ID, GroupID: source.GroupID, BindingID: source.BindingID, OccurrenceID: "source", Requester: requester, Pins: source.Pins, Budget: source.Budget}
			digest, err := original.Digest()
			if err != nil {
				t.Fatal(err)
			}
			sourceGrant := evolution.GenerationGrant{ID: "source-grant", RunID: source.ID, GroupID: source.GroupID, AssignmentDigest: digest}
			if err = store.RetainGenerationGrant(ctx, original, sourceGrant); err != nil {
				t.Fatal(err)
			}
			worker := Worker{Store: store, ID: "source-worker", Authority: executor.Authority, Executor: executor}
			if err = worker.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if provider.calls != 1 {
				t.Fatal(provider.calls)
			}
			provider.outcome = nil
			graph, err := store.Workflow(ctx, source.Pins.Workflow)
			if err != nil {
				t.Fatal(err)
			}
			graph.ID = "owned-draft"
			nodeID := ""
			count := 2
			if kind == "node" {
				nodeID = "agent"
				count = 1
			}
			if kind == "subgraph" {
				nodeID = "container"
				count = 3
				graph.Nodes = []evolution.Node{{ID: nodeID, Kind: "subgraph", InputPorts: graph.Inputs, OutputPorts: graph.Outputs, Body: &source.Pins.Workflow, MaxDepth: 2, MaxAttempts: 1, TimeoutSeconds: 30}}
				graph.Edges = []evolution.Edge{{From: "$input.value", To: "container.value"}, {From: "container.value", To: "$output.value"}}
			}
			draft, err := store.SaveWorkflowDraft(ctx, source.GroupID, projections.WorkflowDraftInput{ID: graph.ID, Workflow: graph})
			if err != nil {
				t.Fatal(err)
			}
			input := projections.WorkflowDraftTestRequest{ID: "test", GroupID: source.GroupID, SourceRunID: source.ID, DraftID: draft.ID, Revision: draft.Revision, Kind: kind, NodeID: nodeID, Budget: source.Budget}
			// Fault the admission insert and prove snapshots and run roll back together.
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TRIGGER fail_test_admission BEFORE INSERT ON generation_admissions BEGIN SELECT RAISE(ABORT,'fault'); END;`); err != nil {
				t.Fatal(err)
			}
			if _, err = store.CreateWorkflowDraftTest(ctx, input, requester); err == nil {
				t.Fatal("admission fault ignored")
			}
			if _, err = store.Run(ctx, source.GroupID, input.ID); !errors.Is(err, projections.ErrNotFound) {
				t.Fatal("orphan run", err)
			}
			var snapshots int
			if err = db.QueryRow("SELECT count(*) FROM workflow_revisions WHERE id LIKE 'draft-test-%' OR id LIKE 'scope-%'").Scan(&snapshots); err != nil || snapshots != 0 {
				t.Fatal("orphan snapshot", snapshots, err)
			}
			if _, err = db.Exec("DROP TRIGGER fail_test_admission"); err != nil {
				t.Fatal(err)
			}
			created, err := store.CreateWorkflowDraftTest(ctx, input, requester)
			if err != nil {
				t.Fatal(err)
			}
			if created.DraftTest == nil || created.Experiment != nil || created.Handoff != "not_applicable" || created.EvaluationState != "not_applicable" {
				t.Fatal(created)
			}
			pins := created.Pins
			pins.Workflow = source.Pins.Workflow
			if !reflect.DeepEqual(pins, source.Pins) || created.DraftTest.Source != sourceGrant {
				t.Fatal("input lineage changed")
			}
			graph.Limits.TimeoutSeconds++
			if _, err = store.SaveWorkflowDraft(ctx, source.GroupID, projections.WorkflowDraftInput{ID: graph.ID, Workflow: graph, ExpectedRevision: draft.Revision, ExpectedLayoutRevision: draft.LayoutRevision}); err != nil {
				t.Fatal(err)
			}
			replay, err := store.CreateWorkflowDraftTest(ctx, input, requester)
			if err != nil || replay.Pins.Workflow != created.Pins.Workflow {
				t.Fatal("lost reply after draft edit", replay, err)
			}
			other := requester
			other.Subject = "other"
			if _, err = store.CreateWorkflowDraftTest(ctx, input, other); !errors.Is(err, projections.ErrConflict) {
				t.Fatal("requester rebound", err)
			}
			// Reopen the database before recovering admission.
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = projections.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			executor.Registry.Store = store
			provider.store = store
			pending, err := store.PendingGenerationAssignment(ctx, created.GroupID, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			digest, err = pending.Digest()
			if err != nil {
				t.Fatal(err)
			}
			grant := evolution.GenerationGrant{ID: "test-grant", RunID: created.ID, GroupID: created.GroupID, AssignmentDigest: digest}
			central := &retryAdmissionCentral{centralGeneration: centralGeneration{assignment: pending, grant: grant, state: "admitted"}, loseReply: true}
			authority := CentralAuthority{Store: store, Central: central}
			executor.Authority = authority
			worker = Worker{Store: store, ID: "test-worker", Authority: authority, Executor: executor}
			if err = worker.Step(ctx); err == nil {
				t.Fatal("lost admission reply ignored")
			}
			if provider.calls != 1 {
				t.Fatal("effects before admission receipt")
			}
			worker = Worker{Store: store, ID: "recovered-test-worker", Authority: authority, Executor: executor}
			if err = worker.Step(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := store.Run(ctx, created.GroupID, created.ID)
			if err != nil || current.State != "completed" || current.Handoff != "not_applicable" || current.EvaluationState != "not_applicable" || provider.calls != 2 {
				t.Fatal(current, err, provider.calls)
			}
			attempts, err := store.WorkflowAttempts(ctx, created.GroupID, created.ID, "", 100)
			if err != nil || len(attempts) != count {
				t.Fatal(attempts, err)
			}
			for _, attempt := range attempts {
				if attempt.ReusedFrom != nil {
					t.Fatal("test reused checkpoint", attempt)
				}
			}
			checkpoint, err := store.LatestRunCheckpoint(ctx, created.GroupID, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := store.Artifact(ctx, checkpoint.Artifact)
			if err != nil {
				t.Fatal(err)
			}
			var output map[string]json.RawMessage
			if err = json.Unmarshal(raw, &output); err != nil {
				t.Fatal(err)
			}
			if kind == "workflow" {
				if string(output["value"]) != `"MODEL OUTPUT"` {
					t.Fatal(string(raw))
				}
			} else {
				if len(output["outputs"]) == 0 || string(output["inactive"]) != "[]" {
					t.Fatal(string(raw))
				}
			}
			if err = authority.AuthorizeRun(ctx, current, false); err != nil {
				t.Fatal("completed test inspection", err)
			}
			if err = authority.ReconcileTerminal(ctx, current); err != nil {
				t.Fatal(err)
			}
			if central.spent.Tokens != 4 || central.spent.CostMicros != 7 {
				t.Fatal("fresh work unaccounted", central.spent)
			}
			handoffs, err := store.PendingHandoffs(ctx, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, handoff := range handoffs {
				if handoff.RunID == created.ID {
					t.Fatal("test entered handoff queue")
				}
			}
			if _, err = store.PrepareHandoff(ctx, created.GroupID, evolution.Patch{RunID: created.ID, Fence: current.Fence, Base: created.Pins.Base}); !errors.Is(err, projections.ErrConflict) {
				t.Fatal("test prepared production patch", err)
			}
			if _, err = store.GenerationOutput(ctx, evolution.GenerationOutputScope{GroupID: created.GroupID, RunID: created.ID, Fence: current.Fence, Port: "value"}); !errors.Is(err, projections.ErrConflict) {
				t.Fatal("test promoted output", err)
			}
			if _, err = store.PreviewRunExperiment(ctx, projections.RunExperimentRequest{GroupID: created.GroupID, SourceRunID: created.ID}); !errors.Is(err, projections.ErrConflict) {
				t.Fatal("test converted into production experiment", err)
			}
			retry, err := store.RetryRun(ctx, projections.RunRetry{GroupID: created.GroupID, RequestID: "retry", SourceRunID: created.ID, NewRunID: "retry-test"}, requester)
			if err != nil {
				t.Fatal(err)
			}
			retryAssignment, err := store.PendingGenerationAssignment(ctx, retry.GroupID, retry.ID)
			if err != nil || !reflect.DeepEqual(retryAssignment.DraftTest, created.DraftTest) || retry.Handoff != "not_applicable" || retry.EvaluationState != "not_applicable" {
				t.Fatal("retry classification lost", retryAssignment, retry, err)
			}
			if err = store.VerifyDraftTestScope(ctx, retry); err != nil {
				t.Fatal(err)
			}
			forged := created
			forged.Pins.Workflow = source.Pins.Workflow
			if err = store.VerifyDraftTestScope(ctx, forged); err == nil {
				t.Fatal("scope substitution accepted")
			}
		})
	}
}

func TestDraftTestAdmissionRejectsInvalidScopeAndRetainsOrdinarySlot(t *testing.T) {
	store, _, source, _, _ := nativeFixture(t)
	ctx := context.Background()
	requester := evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}
	assignment := evolution.GenerationAssignment{RunID: source.ID, GroupID: source.GroupID, BindingID: source.BindingID, OccurrenceID: "source", Requester: requester, Pins: source.Pins, Budget: source.Budget}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "source-grant", RunID: source.ID, GroupID: source.GroupID, AssignmentDigest: digest}
	if err = store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	graph, err := store.Workflow(ctx, source.Pins.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	graph.ID = "owned"
	draft, err := store.SaveWorkflowDraft(ctx, source.GroupID, projections.WorkflowDraftInput{ID: graph.ID, Workflow: graph})
	if err != nil {
		t.Fatal(err)
	}
	input := projections.WorkflowDraftTestRequest{ID: "test", GroupID: source.GroupID, SourceRunID: source.ID, DraftID: draft.ID, Revision: draft.Revision, Kind: "node", NodeID: "agent", Budget: source.Budget}
	for _, tc := range []struct {
		name   string
		mutate func(*projections.WorkflowDraftTestRequest)
	}{
		{"owner", func(r *projections.WorkflowDraftTestRequest) { r.GroupID = "other" }},
		{"revision", func(r *projections.WorkflowDraftTestRequest) { r.Revision++ }},
		{"node", func(r *projections.WorkflowDraftTestRequest) { r.NodeID = "missing" }},
		{"container", func(r *projections.WorkflowDraftTestRequest) { r.Kind = "subgraph" }},
		{"full-node", func(r *projections.WorkflowDraftTestRequest) { r.Kind = "workflow" }},
		{"budget", func(r *projections.WorkflowDraftTestRequest) { r.Budget.MaxTokens++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := input
			tc.mutate(&bad)
			if _, err := store.CreateWorkflowDraftTest(ctx, bad, requester); err == nil {
				t.Fatal("invalid test admitted")
			}
			if _, err := store.Run(ctx, input.GroupID, input.ID); !errors.Is(err, projections.ErrNotFound) {
				t.Fatal("rejected test persisted", err)
			}
		})
	}
	for _, id := range []string{"test-one", "test-two"} {
		input.ID = id
		if _, err = store.CreateWorkflowDraftTest(ctx, input, requester); err != nil {
			t.Fatal("tests blocked by active production run", err)
		}
	}
	current, err := store.Run(ctx, source.GroupID, source.ID)
	if err != nil || current.State != "queued" || current.DraftTest != nil || current.Pins.Workflow != source.Pins.Workflow {
		t.Fatal("source changed", current, err)
	}
}
