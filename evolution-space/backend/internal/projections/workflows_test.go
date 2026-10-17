package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowCatalogImmutableAndRetained(t *testing.T) {
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
	raw, err = workflow.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	ref := evolution.Revision{ID: workflow.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = s.PublishWorkflow(ctx, ref, raw); err != nil {
		t.Fatal(err)
	}
	if err = s.PublishWorkflow(ctx, ref, raw); err != nil {
		t.Fatal("retry", err)
	}
	skipped := ref
	skipped.Revision = 3
	if err = s.PublishWorkflow(ctx, skipped, raw); err != ErrConflict {
		t.Fatal("skipped revision", err)
	}
	changed := ref
	changed.Digest = evolution.Digest([]byte("other"))
	if _, err = s.Workflow(ctx, changed); err == nil {
		t.Fatal("digest substitution")
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM workflow_revisions"); err == nil {
		t.Fatal("deleted provenance")
	}
	root := workflow
	root.ID = "nested-workflow"
	root.Nodes = []evolution.Node{{ID: "nested", Kind: "subgraph", Body: &ref, InputPorts: workflow.Inputs, OutputPorts: workflow.Outputs, MaxDepth: 4, MaxAttempts: 1, TimeoutSeconds: 120}}
	root.Edges = nil
	for name := range workflow.Inputs {
		root.Edges = append(root.Edges, evolution.Edge{From: "$input." + name, To: "nested." + name})
	}
	for name := range workflow.Outputs {
		root.Edges = append(root.Edges, evolution.Edge{From: "nested." + name, To: "$output." + name})
	}
	rootRaw, err := root.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	rootRef := evolution.Revision{ID: root.ID, Revision: 1, Digest: evolution.Digest(rootRaw)}
	if err = s.PublishWorkflow(ctx, rootRef, rootRaw); err != nil {
		t.Fatal("valid closure", err)
	}
	root.ID = "missing-nested"
	missing := ref
	missing.Revision = 2
	root.Nodes[0].Body = &missing
	rootRaw, err = root.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	rootRef = evolution.Revision{ID: root.ID, Revision: 1, Digest: evolution.Digest(rootRaw)}
	if err = s.PublishWorkflow(ctx, rootRef, rootRaw); err == nil {
		t.Fatal("missing immutable nested revision accepted")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := s.Workflow(ctx, ref)
	if err != nil || retained.ID != workflow.ID {
		t.Fatal("restart lost workflow", err)
	}
}
