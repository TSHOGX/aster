package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExperimentFreezesEditedWorkflowAndLeavesPausedSource(t *testing.T) {
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
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = workflow.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	ref := evolution.Revision{ID: workflow.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = s.PublishWorkflow(ctx, ref, raw); err != nil {
		t.Fatal(err)
	}
	artifact := evolution.ArtifactRef{ID: "input", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("input")), Size: 5, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	if err = s.SealArtifact(ctx, artifact, strings.NewReader("input")); err != nil {
		t.Fatal(err)
	}
	pins := evolution.RunPins{Workflow: ref, Inputs: []evolution.ArtifactRef{artifact}, InputPorts: map[string]evolution.ArtifactRef{}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
	source := evolution.Run{ID: "source", GroupID: "group", BindingID: "binding", Pins: pins, State: "paused", Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	body, _ := json.Marshal(source)
	if _, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", source.ID, source.GroupID, source.BindingID, 1, "source-occurrence", []byte(`{"source":true}`), body, source.State); err != nil {
		t.Fatal(err)
	}
	checkpoint := RunCheckpoint{ID: "checkpoint", RunID: source.ID, Artifact: artifact}
	checkpointRaw, _ := json.Marshal(checkpoint)
	if _, err = s.db.ExecContext(ctx, "INSERT INTO run_checkpoints(run_id,id,sequence,body) VALUES(?,?,?,?)", source.ID, checkpoint.ID, 1, checkpointRaw); err != nil {
		t.Fatal(err)
	}
	edited := workflow
	edited.Nodes[0].Prompt.Text = "changed {{corpus}}"
	edited.Nodes[0].Prompt.Digest = evolution.Digest([]byte(edited.Nodes[0].Prompt.Text))
	preview, err := s.PreviewRunExperiment(ctx, source.GroupID, source.ID, checkpoint.ID)
	if err != nil || !preview.Eligible || len(preview.Dependencies) != 1 {
		t.Fatal(preview, err)
	}
	created, err := s.CreateRunExperiment(ctx, RunExperimentRequest{ID: "experiment", GroupID: source.GroupID, SourceRunID: source.ID, CheckpointID: checkpoint.ID, Workflow: edited})
	if err != nil {
		t.Fatal(err)
	}
	if created.ExperimentOf != source.ID || created.ID == source.ID || created.State != "queued" || created.Pins.Workflow.ID == workflow.ID {
		t.Fatal(created)
	}
	retained, err := s.Run(ctx, source.GroupID, source.ID)
	if err != nil || retained.State != "paused" || retained.Pins.Workflow != ref {
		t.Fatal("source changed", retained, err)
	}
	replay, err := s.CreateRunExperiment(ctx, RunExperimentRequest{ID: "experiment", GroupID: source.GroupID, SourceRunID: source.ID, CheckpointID: checkpoint.ID, Workflow: edited})
	if err != nil || replay.ID != created.ID || replay.Pins.Workflow != created.Pins.Workflow {
		t.Fatal("experiment replay changed identity", replay, err)
	}
}
