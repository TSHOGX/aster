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

func TestWorkflowLayoutRevisionDoesNotChangeExecutableContent(t *testing.T) {
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
	_, err = s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, Layout: json.RawMessage(`{"x":1}`), Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, ExpectedLayoutRevision: 1, Layout: json.RawMessage(`{"x":2}`), Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || second.LayoutRevision != 2 {
		t.Fatal(second)
	}
	if string(second.Layout) != `{"x":2}` {
		t.Fatal(string(second.Layout))
	}
	if _, err = s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 2, ExpectedLayoutRevision: 1, Layout: json.RawMessage(`{"x":3}`), Workflow: workflow}); err != ErrConflict {
		t.Fatal("layout stale CAS", err)
	}
	ref, err := s.PublishWorkflowDraft(ctx, workflow.ID, second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := workflow.Canonical()
	if ref.Digest != evolution.Digest(body) {
		t.Fatal("layout changed executable digest")
	}
}

func TestWorkflowLayoutStrictBoundedObject(t *testing.T) {
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
	for _, layout := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`{"x":1,"x":2}`), json.RawMessage(strings.Repeat("x", evolution.MaxDocumentBytes))} {
		if _, err = s.SaveWorkflowDraft(ctx, WorkflowDraftInput{ID: workflow.ID, Layout: layout, Workflow: workflow}); err == nil {
			t.Fatal("invalid layout accepted")
		}
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM workflow_draft_revisions WHERE id=?", workflow.ID); err == nil {
		t.Fatal("draft history deleted")
	}

}
