package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowDraftHistoryReadsExactLayoutRevision(t *testing.T) {
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
	w, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: w.ID, Layout: []byte(`{"x":1}`), Workflow: w})
	if err != nil {
		t.Fatal(err)
	}
	workflowPrompt(w).Text = "changed {{corpus}}"
	workflowPrompt(w).Digest = evolution.Digest([]byte(workflowPrompt(w).Text))
	if _, err = s.SaveWorkflowDraft(ctx, "team", WorkflowDraftInput{ID: w.ID, ExpectedRevision: first.Revision, ExpectedLayoutRevision: first.LayoutRevision, Layout: []byte(`{"x":2}`), Workflow: w}); err != nil {
		t.Fatal(err)
	}
	old, err := s.WorkflowDraftRevision(ctx, "team", w.ID, 1)
	if err != nil || workflowPrompt(old.Workflow).Text == "changed {{corpus}}" || string(old.Layout) != `{"x":1}` {
		t.Fatal(old, err)
	}
	if _, err = s.WorkflowDraftRevision(ctx, "team", w.ID, 3); err == nil {
		t.Fatal("missing history accepted")
	}
}
