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
	draft, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 1 {
		t.Fatal(draft)
	}
	if _, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 0, Workflow: workflow}); err != ErrConflict {
		t.Fatal("stale draft", err)
	}
	workflowPrompt(workflow).Text = "changed {{corpus}}"
	workflowPrompt(workflow).Digest = evolution.Digest([]byte(workflowPrompt(workflow).Text))
	next, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, ExpectedLayoutRevision: 1, Workflow: workflow})
	if err != nil || next.Revision != 2 {
		t.Fatal(next, err)
	}
	read, err := s.WorkflowDraft(ctx, "team", workflow.ID)
	if err != nil || read.Revision != 2 || workflowPrompt(read.Workflow).Text != "changed {{corpus}}" {
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
	_, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, Layout: json.RawMessage(`{"x":1}`), Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, ExpectedLayoutRevision: 1, Layout: json.RawMessage(`{"x":2}`), Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 1 || second.LayoutRevision != 2 {
		t.Fatal(second)
	}
	if string(second.Layout) != `{"x":2}` {
		t.Fatal(string(second.Layout))
	}
	if _, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, ExpectedLayoutRevision: 1, Layout: json.RawMessage(`{"x":3}`), Workflow: workflow}); err != ErrConflict {
		t.Fatal("layout stale CAS", err)
	}
	ref, err := s.PublishWorkflowDraft(ctx, "team", workflow.ID, second.Revision)
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
		if _, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, Layout: layout, Workflow: workflow}); err == nil {
			t.Fatal("invalid layout accepted")
		}
	}
	if _, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, Workflow: workflow}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM workflow_draft_revisions WHERE id=?", workflow.ID); err == nil {
		t.Fatal("draft history deleted")
	}

}

func workflowPrompt(w evolution.Workflow) *evolution.Prompt {
	for _, node := range w.Nodes {
		if node.ID == "extract" {
			return node.Prompt
		}
	}
	panic("fixture is missing extract node")
}

func TestIncompleteWorkflowDraftPersistsButCannotPublish(t *testing.T) {
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
	edges := graph.Edges
	graph.Edges = nil
	draft, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: graph.ID, Workflow: graph})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := s.WorkflowDraft(ctx, "team", graph.ID)
	if err != nil || len(retained.Workflow.Edges) != 0 {
		t.Fatal("incomplete draft lost", retained, err)
	}
	if _, err = s.PublishWorkflowDraft(ctx, "team", graph.ID, draft.Revision); err == nil {
		t.Fatal("disconnected draft published")
	}
	graph.Edges = edges
	repaired, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: graph.ID, Workflow: graph, ExpectedRevision: draft.Revision, ExpectedLayoutRevision: draft.LayoutRevision})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.PublishWorkflowDraft(ctx, "team", graph.ID, repaired.Revision)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := graph.Canonical()
	if ref.Digest != evolution.Digest(canonical) {
		t.Fatal("draft normalization changed executable identity", ref)
	}
	old, err := s.WorkflowDraftRevision(ctx, "team", graph.ID, draft.Revision)
	if err != nil || len(old.Workflow.Edges) != 0 {
		t.Fatal("repair rewrote incomplete history", old, err)
	}
}
