package workflow

import (
	"aster/contracts/evolution"
	"bytes"
	"fmt"
	"strings"
)

// DraftScope produces an executable dependency closure from a validated draft.
// Every input schema is retained so the source run's exact input pins still apply.
// A partial scope exposes the selected node's ports, including inactive routes.
func DraftScope(draft evolution.Workflow, kind, nodeID, id string) (evolution.Workflow, error) {
	raw, err := draft.Canonical()
	if err != nil {
		return evolution.Workflow{}, err
	}
	result, err := evolution.Decode[evolution.Workflow](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	result.ID = id
	if kind == "workflow" {
		if nodeID != "" {
			return result, fmt.Errorf("full workflow test cannot select a node")
		}
		return result, result.Validate()
	}
	if kind != "node" && kind != "subgraph" {
		return result, fmt.Errorf("invalid draft test scope")
	}
	var selected *evolution.Node
	for i := range result.Nodes {
		if result.Nodes[i].ID == nodeID {
			selected = &result.Nodes[i]
			break
		}
	}
	if selected == nil {
		return result, evolution.FieldError{Path: "node_id", Message: "selected node does not exist"}
	}
	if kind == "subgraph" && selected.Kind != "subgraph" && selected.Kind != "map" && selected.Kind != "loop" {
		return result, evolution.FieldError{Path: "node_id", Message: "subgraph scope requires a subgraph, map or loop container"}
	}
	if len(selected.OutputPorts) == 0 {
		return result, evolution.FieldError{Path: "node_id", Message: "test node requires observable output ports"}
	}
	keep := map[string]bool{nodeID: true}
	for changed := true; changed; {
		changed = false
		for _, edge := range result.Edges {
			from, to := strings.SplitN(edge.From, ".", 2)[0], strings.SplitN(edge.To, ".", 2)[0]
			if keep[to] && from != "$input" && !keep[from] {
				keep[from], changed = true, true
			}
		}
	}
	nodes := []evolution.Node{}
	for _, node := range result.Nodes {
		if keep[node.ID] {
			nodes = append(nodes, node)
		}
	}
	edges := []evolution.Edge{}
	for _, edge := range result.Edges {
		if keep[strings.SplitN(edge.To, ".", 2)[0]] {
			edges = append(edges, edge)
		}
	}
	result.Outputs = selected.OutputPorts
	for name := range result.Outputs {
		edges = append(edges, evolution.Edge{From: nodeID + "." + name, To: "$output." + name})
	}
	result.Nodes, result.Edges = nodes, edges
	return result, result.Validate()
}
