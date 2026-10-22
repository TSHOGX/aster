package evolution

import (
	"context"
	"fmt"
)

// WorkflowResolver retrieves exact published revisions. A mutable draft or latest
// revision lookup cannot satisfy an immutable body reference.
type WorkflowResolver interface {
	ResolveWorkflow(context.Context, Revision) (Workflow, error)
}

// ValidateClosure validates every referenced body and its executable digest before
// publication or admission. Limits apply across the entire expansion, not merely
// to each individually valid graph.
func ValidateClosure(ctx context.Context, root Workflow, resolver WorkflowResolver) error {
	active := map[string]bool{}
	documents := 0
	var visit func(Workflow, int) error
	visit = func(w Workflow, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 16 {
			return fmt.Errorf("workflow nesting exceeds 16")
		}
		documents++
		if documents > 1000 {
			return fmt.Errorf("workflow expansion exceeds 1000 documents")
		}
		if err := w.Validate(); err != nil {
			return err
		}
		for _, n := range w.Nodes {
			if n.Body == nil {
				continue
			}
			if resolver == nil {
				return FieldError{n.ID + ".body", "immutable workflow resolver required"}
			}
			key := fmt.Sprintf("%s:%d:%s", n.Body.ID, n.Body.Revision, n.Body.Digest)
			if active[key] {
				return FieldError{n.ID + ".body", "recursive workflow reference"}
			}
			body, err := resolver.ResolveWorkflow(ctx, *n.Body)
			if err != nil {
				return err
			}
			raw, err := body.Canonical()
			if err != nil {
				return err
			}
			if body.ID != n.Body.ID || Digest(raw) != n.Body.Digest {
				return FieldError{n.ID + ".body", "resolved workflow identity mismatch"}
			}
			inputs, outputs := n.InputPorts, n.OutputPorts
			if n.Kind == "map" {
				inputs, outputs = n.ItemInputs, n.ItemOutputs
			}
			if !equalPorts(inputs, body.Inputs) || !equalPorts(outputs, body.Outputs) {
				return FieldError{n.ID + ".body", "body interface does not match node ports"}
			}
			if n.Kind == "subgraph" && depth+1 > n.MaxDepth {
				return FieldError{n.ID + ".max_depth", "subgraph nesting limit exceeded"}
			}
			active[key] = true
			err = visit(body, depth+1)
			delete(active, key)
			if err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root, 0)
}
func equalPorts(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
