package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowDraftCASAndPublishSeparation(t *testing.T) {
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
	draft, err := s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 1 {
		t.Fatal(draft)
	}
	if _, err = s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 0, Workflow: workflow}); err != ErrConflict {
		t.Fatal("stale draft", err)
	}
	workflow.Nodes[0].Prompt.Text = "changed"
	next, err := s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, Workflow: workflow})
	if err != nil || next.Revision != 2 {
		t.Fatal(next, err)
	}
	read, err := s.WorkflowDraft(ctx, workflow.ID)
	if err != nil || read.Revision != 2 || read.Workflow.Nodes[0].Prompt.Text != "changed" {
		t.Fatal(read, err)
	}
	if _, err = s.Workflow(ctx, evolution.Revision{ID: workflow.ID, Revision: 1, Digest: evolution.Digest(raw)}); err == nil {
		t.Fatal("draft implicitly published")
	}
}
