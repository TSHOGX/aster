package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowDraftOwnershipSurvivesPublicationAndRestart(t *testing.T) {
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
	graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := graph.Canonical()
	ref := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(canonical)}
	if err = s.PublishWorkflow(ctx, ref, canonical); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveWorkflowDraft(ctx, "alpha", WorkflowDraftInput{ID: graph.ID, Workflow: graph}); err != ErrConflict {
		t.Fatal("claimed catalog workflow", err)
	}
	graph.ID = "alpha-skills"
	input := WorkflowDraftInput{ID: graph.ID, Workflow: graph}
	draft, err := s.SaveWorkflowDraft(ctx, "alpha", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, beforePublish := range []bool{true, false} {
		if !beforePublish {
			if _, err = s.PublishWorkflowDraft(ctx, "alpha", graph.ID, draft.Revision); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.WorkflowDraft(ctx, "beta", graph.ID); err != ErrNotFound {
			t.Fatal("cross-group read", err)
		}
		if _, err = s.WorkflowDraftRevision(ctx, "beta", graph.ID, draft.Revision); err != ErrNotFound {
			t.Fatal("cross-group history", err)
		}
		if _, err = s.PublishWorkflowDraft(ctx, "beta", graph.ID, draft.Revision); err != ErrNotFound {
			t.Fatal("cross-group publication or replay", err)
		}
		for _, revision := range []int64{0, draft.Revision} {
			input.ExpectedRevision, input.ExpectedLayoutRevision = revision, revision
			if _, err = s.SaveWorkflowDraft(ctx, "beta", input); err != ErrNotFound {
				t.Fatal("cross-group edit", err)
			}
		}
		retained, err := s.WorkflowDraft(ctx, "alpha", graph.ID)
		if err != nil || retained.Revision != draft.Revision {
			t.Fatal("owner data changed", retained, err)
		}
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE workflow_drafts SET group_id='beta' WHERE id=?", graph.ID); err == nil {
		t.Fatal("ownership changed")
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM workflow_drafts WHERE id=?", graph.ID); err == nil {
		t.Fatal("ownership removed")
	}
}
