package projections

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/workflow"
)

// PlanCheckpointReuse selects only strict ancestors of the declared start node.
// A changed ancestor requires selecting an earlier start node. Unrelated branches
// and the selected node always execute afresh, including nondeterministic agents.
// Nested work is reused only through a completed container's validated outputs;
// its immutable body reference is part of the container's semantic identity.
func PlanCheckpointReuse(source, edited evolution.Workflow, start string, records []workflow.Record) ([]workflow.Record, error) {
	if source.Validate() != nil || edited.Validate() != nil {
		return nil, fmt.Errorf("invalid experiment workflow")
	}
	same := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }
	if !same(source.Inputs, edited.Inputs) || !same(source.ParametersSchema, edited.ParametersSchema) {
		return nil, fmt.Errorf("experiment input contract changed")
	}
	oldNodes, newNodes := map[string]evolution.Node{}, map[string]evolution.Node{}
	for _, n := range source.Nodes {
		oldNodes[n.ID] = n
	}
	for _, n := range edited.Nodes {
		newNodes[n.ID] = n
	}
	if _, ok := newNodes[start]; !ok {
		return nil, fmt.Errorf("experiment start node missing")
	}
	incoming := func(w evolution.Workflow) map[string]map[string]string {
		result := map[string]map[string]string{}
		for _, edge := range w.Edges {
			node, _, _ := strings.Cut(edge.To, ".")
			if result[node] == nil {
				result[node] = map[string]string{}
			}
			result[node][edge.To] = edge.From
		}
		return result
	}
	oldEdges, newEdges := incoming(source), incoming(edited)
	ancestors := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		for _, from := range newEdges[id] {
			parent, _, _ := strings.Cut(from, ".")
			if parent == "$input" || ancestors[parent] {
				continue
			}
			ancestors[parent] = true
			visit(parent)
		}
	}
	visit(start)
	retained := map[string]workflow.Record{}
	for _, record := range records {
		if record.Call.Instance != "root/"+record.Call.Node.ID || record.State != "succeeded" && record.State != "skipped" {
			continue
		}
		if old, ok := retained[record.Call.Node.ID]; !ok || old.Call.Number < record.Call.Number {
			retained[record.Call.Node.ID] = record
		}
	}
	ids := make([]string, 0, len(ancestors))
	for id := range ancestors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]workflow.Record, 0, len(ids))
	for _, id := range ids {
		previous, exists := oldNodes[id]
		if !exists || !same(previous, newNodes[id]) || !same(oldEdges[id], newEdges[id]) {
			return nil, fmt.Errorf("upstream semantics changed: %s", id)
		}
		record, exists := retained[id]
		if !exists || !same(record.Call.Node, previous) {
			return nil, fmt.Errorf("upstream checkpoint output unavailable: %s", id)
		}
		result = append(result, record)
	}
	return result, nil
}
