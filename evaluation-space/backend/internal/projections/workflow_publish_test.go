package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishWorkflowDraftPinsNextImmutableRevision(t *testing.T) {
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
	ref, err := s.PublishWorkflowDraft(ctx, workflow.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Revision != 1 {
		t.Fatal(ref)
	}
	if _, err = s.PublishWorkflowDraft(ctx, workflow.ID, draft.Revision); err != ErrConflict {
		t.Fatal("stale draft republished", err)
	}
	workflow.Nodes[0].Prompt.Text = "changed"
	next, err := s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: draft.Revision, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	ref, err = s.PublishWorkflowDraft(ctx, workflow.ID, next.Revision)
	if err != nil || ref.Revision != 2 {
		t.Fatal(ref, err)
	}
	published, err := s.Workflow(ctx, ref)
	if err != nil || published.Nodes[0].Prompt.Text != "changed" {
		t.Fatal(published, err)
	}
	old, err := s.Workflow(ctx, evolution.Revision{ID: workflow.ID, Revision: 1, Digest: ref.Digest})
	if err == nil || old.ID != "" {
		t.Fatal("old identity substituted", err)
	}
}
