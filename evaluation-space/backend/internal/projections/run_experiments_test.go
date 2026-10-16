package projections

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/workflow"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunExperimentFreezesCheckpointAndAdmissionAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = graph.Canonical()
	ref := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = s.PublishWorkflow(ctx, ref, raw); err != nil {
		t.Fatal(err)
	}
	artifact := evolution.ArtifactRef{ID: "input", GroupID: "group", SchemaDigest: graph.Inputs["corpus"], Digest: evolution.Digest([]byte(`[]`)), Size: 2, MediaType: "application/json", GrantID: "input-grant", PolicyRevision: 1}
	if err = s.SealArtifact(ctx, artifact, strings.NewReader(`[]`)); err != nil {
		t.Fatal(err)
	}
	pins := evolution.RunPins{Workflow: ref, SourcePolicyRevision: 1, Inputs: []evolution.ArtifactRef{artifact}, InputPorts: map[string]evolution.ArtifactRef{"corpus": artifact}, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
	source := evolution.Run{ID: "source", GroupID: "group", BindingID: "binding", BatchID: "batch", Pins: pins, State: "queued", Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 10, TimeoutSeconds: 30, MaxArtifactBytes: 1 << 20}}
	body, _ := json.Marshal(source)
	if _, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", source.ID, source.GroupID, source.BindingID, 1, "source-occurrence", []byte(`{}`), body, source.State); err != nil {
		t.Fatal(err)
	}
	requester := evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}
	assignment := evolution.GenerationAssignment{RunID: source.ID, GroupID: source.GroupID, BindingID: source.BindingID, BatchID: source.BatchID, OccurrenceID: "source-occurrence", Requester: requester, Pins: pins, Budget: source.Budget}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "source-grant", RunID: source.ID, GroupID: source.GroupID, AssignmentDigest: digest}
	if err = s.RetainGenerationGrant(ctx, assignment, grant); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, source.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	journal := WorkflowJournal{Store: s, Lease: lease, GrantID: grant.ID}
	call := workflow.Call{ID: "extract", Instance: "root/extract", Number: 1, Workflow: ref, Node: graph.Nodes[0], Inputs: workflow.Values{"corpus": json.RawMessage(`[]`)}, Prompt: "Extract []"}
	record, err := journal.Begin(ctx, call, 10)
	if err != nil {
		t.Fatal(err)
	}
	record.State, record.Outputs = "succeeded", workflow.Values{"items": json.RawMessage(`[]`)}
	if err = journal.Complete(ctx, record); err != nil {
		t.Fatal(err)
	}
	paused, err := s.ControlRun(ctx, RunControl{RequestID: "pause", RunID: source.ID, ExpectedFence: lease.Fence, Action: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	input := RunExperimentRequest{ID: "experiment", GroupID: source.GroupID, SourceRunID: source.ID, CheckpointID: "node-extract", StartNode: "critic", Workflow: graph}
	input.Workflow.Nodes[1].TimeoutSeconds++
	preview, err := s.PreviewRunExperiment(ctx, input)
	if err != nil || !preview.Eligible || len(preview.Reused) != 1 || len(preview.Dependencies) != 2 {
		t.Fatal(preview, err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_experiment_admission BEFORE INSERT ON generation_admissions BEGIN SELECT RAISE(ABORT,'fault'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRunExperiment(ctx, input, requester); err == nil {
		t.Fatal("admission fault ignored")
	}
	if _, err = s.Run(ctx, source.GroupID, input.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("run escaped admission rollback", err)
	}
	if _, err = s.db.ExecContext(ctx, "DROP TRIGGER fail_experiment_admission"); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateRunExperiment(ctx, input, requester)
	if err != nil || created.ExperimentOf != source.ID || created.State != "queued" || created.BatchID != source.BatchID || created.Experiment == nil || created.Experiment.Checkpoint != preview.Checkpoint.Artifact {
		t.Fatal(created, err)
	}
	pending, err := s.PendingGenerationAssignment(ctx, source.GroupID, created.ID)
	if err != nil || pending.Experiment == nil || pending.Experiment.Source != grant || pending.Requester != requester || pending.Pins.Workflow != created.Pins.Workflow {
		t.Fatal(pending, err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "resume", RunID: source.ID, ExpectedFence: paused.Fence, Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateRunExperiment(ctx, input, requester)
	if err != nil || replay.ID != created.ID || replay.Pins.Workflow != created.Pins.Workflow {
		t.Fatal("lost reply changed experiment", replay, err)
	}
	other := requester
	other.Subject = "other"
	if _, err = s.CreateRunExperiment(ctx, input, other); !errors.Is(err, ErrConflict) {
		t.Fatal("requester rebound", err)
	}
	plan, err := s.ExperimentReuse(ctx, created)
	if err != nil || len(plan) != 1 || plan[0].Call.ID != "extract" {
		t.Fatal("source resume changed immutable reuse", plan, err)
	}
	retained, err := s.Run(ctx, source.GroupID, source.ID)
	if err != nil || retained.Pins.Workflow != ref || retained.ExperimentOf != "" {
		t.Fatal("source changed", retained, err)
	}
}
