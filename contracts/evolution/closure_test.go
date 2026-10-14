package evolution

import (
	"context"
	"fmt"
	"testing"
)

type fixtureResolver map[string]Workflow

func (r fixtureResolver) ResolveWorkflow(_ context.Context, ref Revision) (Workflow, error) {
	w, ok := r[ref.ID]
	if !ok {
		return w, fmt.Errorf("revision unavailable")
	}
	return w, nil
}
func nestedFixture(t *testing.T) (Workflow, Workflow) {
	t.Helper()
	body := simpleWorkflow()
	raw, err := body.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	root := simpleWorkflow()
	root.ID = "skills"
	root.Nodes = []Node{{ID: "revise", Kind: "subgraph", Body: &Revision{body.ID, 1, Digest(raw)}, InputPorts: body.Inputs, OutputPorts: body.Outputs, MaxDepth: 4, MaxAttempts: 1, TimeoutSeconds: 120}}
	root.Edges = []Edge{{"$input.corpus", "revise.corpus"}, {"revise.patch", "$output.patch"}}
	return root, body
}
func TestImmutableWorkflowClosure(t *testing.T) {
	root, body := nestedFixture(t)
	if err := ValidateClosure(context.Background(), root, fixtureResolver{body.ID: body}); err != nil {
		t.Fatal(err)
	}
	body.Nodes[0].Prompt.Text = "changed"
	if err := ValidateClosure(context.Background(), root, fixtureResolver{body.ID: body}); err == nil {
		t.Fatal("changed immutable body accepted")
	}
}
func TestWorkflowClosureMissingAndInterface(t *testing.T) {
	root, body := nestedFixture(t)
	if err := ValidateClosure(context.Background(), root, nil); err == nil {
		t.Fatal("unresolved reference accepted")
	}
	root.Nodes[0].InputPorts = map[string]string{"corpus": Digest([]byte("different"))}
	root.Inputs = root.Nodes[0].InputPorts
	if err := ValidateClosure(context.Background(), root, fixtureResolver{body.ID: body}); err == nil {
		t.Fatal("incompatible body accepted")
	}
}
