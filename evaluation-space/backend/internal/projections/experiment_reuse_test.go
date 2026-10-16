package projections

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/workflow"
)

func TestCheckpointReuseRequiresUnchangedCompleteAncestors(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	records := []workflow.Record{
		{Call: workflow.Call{ID: "failed-extract", Instance: "root/extract", Number: 1, Node: source.Nodes[0]}, State: "failed", ErrorCode: "retryable_failure"},
		{Call: workflow.Call{ID: "extract", Instance: "root/extract", Number: 2, Node: source.Nodes[0]}, State: "succeeded", Outputs: workflow.Values{"items": json.RawMessage(`[]`)}},
		{Call: workflow.Call{ID: "critic", Instance: "root/critic", Number: 1, Node: source.Nodes[1]}, State: "succeeded", Outputs: workflow.Values{"items": json.RawMessage(`[]`)}},
	}
	edited, _ := evolution.DecodeWorkflow(bytes.NewReader(raw))
	edited.ID = "experiment"
	plan, err := PlanCheckpointReuse(source, edited, "build", records)
	if err != nil || len(plan) != 2 || plan[1].Call.ID != "extract" || plan[1].Call.Number != 2 {
		t.Fatal(plan, err)
	}
	// Editing the selected node is allowed; editing its required upstream agent
	// invalidates explicit reuse even when output bytes happen to be unchanged.
	edited.Nodes[1].TimeoutSeconds++
	plan, err = PlanCheckpointReuse(source, edited, "critic", records)
	if err != nil || len(plan) != 1 || plan[0].Call.Node.ID != "extract" {
		t.Fatal(plan, err)
	}
	if _, err = PlanCheckpointReuse(source, edited, "build", records); err == nil {
		t.Fatal("changed ancestor reused")
	}
	edited.Nodes[0].Prompt.Text = "Changed {{corpus}}"
	edited.Nodes[0].Prompt.Digest = evolution.Digest([]byte(edited.Nodes[0].Prompt.Text))
	if _, err = PlanCheckpointReuse(source, edited, "critic", records); err == nil {
		t.Fatal("changed agent reused")
	}
	plan, err = PlanCheckpointReuse(source, edited, "extract", records)
	if err != nil || len(plan) != 0 {
		t.Fatal("first-node start should run afresh", plan, err)
	}
	edited, _ = evolution.DecodeWorkflow(bytes.NewReader(raw))
	if _, err = PlanCheckpointReuse(source, edited, "build", records[:2]); err == nil {
		t.Fatal("incomplete upstream reused")
	}
	if _, err = PlanCheckpointReuse(source, edited, "critic", records[:1]); err == nil {
		t.Fatal("failed upstream reused")
	}
	edited.Edges[1].From = "$input.corpus"
	if _, err = PlanCheckpointReuse(source, edited, "build", records); err == nil {
		t.Fatal("rewired ancestor reused")
	}
	if _, err = PlanCheckpointReuse(source, edited, "missing", records); err == nil {
		t.Fatal("undeclared start accepted")
	}
}
