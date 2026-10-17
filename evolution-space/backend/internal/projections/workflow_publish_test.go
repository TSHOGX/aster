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
	draft, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.PublishWorkflowDraft(ctx, "team", workflow.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Revision != 1 {
		t.Fatal(ref)
	}
	firstRef := ref
	if replay, e := s.PublishWorkflowDraft(ctx, "team", workflow.ID, draft.Revision); e != nil || replay != ref {
		t.Fatal("stale draft republished", err)
	}
	workflowPrompt(workflow).Text = "changed {{corpus}}"
	workflowPrompt(workflow).Digest = evolution.Digest([]byte(workflowPrompt(workflow).Text))
	next, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: draft.Revision, ExpectedLayoutRevision: draft.LayoutRevision, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	ref, err = s.PublishWorkflowDraft(ctx, "team", workflow.ID, next.Revision)
	if err != nil || ref.Revision != 2 {
		t.Fatal(ref, err)
	}
	published, err := s.Workflow(ctx, ref)
	if err != nil || workflowPrompt(published).Text != "changed {{corpus}}" {
		t.Fatal(published, err)
	}
	if old, err := s.Workflow(ctx, firstRef); err != nil || workflowPrompt(old).Text == "changed {{corpus}}" {
		t.Fatal("published history changed", err)
	}
	old, err := s.Workflow(ctx, evolution.Revision{ID: workflow.ID, Revision: 1, Digest: ref.Digest})
	if err == nil || old.ID != "" {
		t.Fatal("old identity substituted", err)
	}
}

func TestWorkflowPublicationConcurrentReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
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
	type outcome struct {
		ref evolution.Revision
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ref, err := s.PublishWorkflowDraft(ctx, "team", workflow.ID, draft.Revision)
			results <- outcome{ref, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.ref != second.ref {
		t.Fatal(first, second)
	}
	layout, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, ExpectedLayoutRevision: 1, Layout: []byte(`{"x":5}`), Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Revision != 1 || layout.LayoutRevision != 2 {
		t.Fatal(layout)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.PublishWorkflowDraft(ctx, "team", workflow.ID, 1)
	if err != nil || replay != first.ref {
		t.Fatal(replay, err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM workflow_revisions").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate executable revision", count, err)
	}
	workflowPrompt(workflow).Text = "changed {{corpus}}"
	workflowPrompt(workflow).Digest = evolution.Digest([]byte(workflowPrompt(workflow).Text))
	draft, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 1, ExpectedLayoutRevision: 2, Layout: layout.Layout, Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 2 || draft.LayoutRevision != 2 {
		t.Fatal("execution edit changed layout revision", draft)
	}
	workflowPrompt(workflow).Text = "changed again {{corpus}}"
	workflowPrompt(workflow).Digest = evolution.Digest([]byte(workflowPrompt(workflow).Text))
	if _, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: workflow.ID, ExpectedRevision: 2, ExpectedLayoutRevision: 2, Layout: layout.Layout, Workflow: workflow}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishWorkflowDraft(ctx, "team", workflow.ID, 2); err != ErrConflict {
		t.Fatal("stale unpublished draft accepted", err)
	}
}
