package workflow

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestDraftScopeExecutesOnlySelectedAncestors(t *testing.T) {
	engine, c, j, schema := setup()
	graph := base("draft", schema, []evolution.Node{transform("before", schema), transform("selected", schema), transform("after", schema), transform("unrelated", schema)}, []evolution.Edge{{From: "$input.value", To: "before.value"}, {From: "before.value", To: "selected.value"}, {From: "selected.value", To: "after.value"}, {From: "$input.value", To: "unrelated.value"}, {From: "after.value", To: "$output.value"}})
	original, _ := graph.Canonical()
	graph.Inputs["unused"] = schema
	scope, err := DraftScope(graph, "node", "selected", "scope")
	if err != nil || len(scope.Nodes) != 2 || len(scope.Inputs) != 2 {
		t.Fatal(scope, err)
	}
	ref := add(c, scope)
	calls := []string{}
	engine.Runtime = runtimeFunc(func(_ context.Context, call Call) (Values, error) {
		calls = append(calls, call.Node.ID)
		return call.Inputs, nil
	})
	result, err := engine.ExecuteDraftScope(context.Background(), ref, Values{"value": json.RawMessage(`"input"`), "unused": json.RawMessage(`null`)})
	if err != nil || string(result.Outputs["value"]) != `"input"` || len(result.Inactive) != 0 || !reflect.DeepEqual(calls, []string{"before", "selected"}) || len(j.records) != 2 {
		t.Fatal(result, err, calls, j.records)
	}
	scope.Nodes[0].InputPorts["value"] = "changed"
	delete(graph.Inputs, "unused")
	after, _ := graph.Canonical()
	if string(original) != string(after) {
		t.Fatal("scope mutated draft")
	}
	for _, tc := range []struct{ kind, node string }{{"node", "missing"}, {"subgraph", "selected"}, {"workflow", "selected"}, {"other", "selected"}} {
		if _, err = DraftScope(graph, tc.kind, tc.node, "scope"); err == nil {
			t.Fatal("invalid scope accepted", tc)
		}
	}
	graph.Edges = append(graph.Edges, evolution.Edge{From: "after.value", To: "before.value"})
	if _, err = DraftScope(graph, "node", "selected", "scope"); err == nil {
		t.Fatal("invalid graph accepted")
	}
}

func TestDraftScopePreservesInactiveRouterEvidence(t *testing.T) {
	engine, c, j, schema := setup()
	router := evolution.Node{ID: "route", Kind: "router", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"yes": schema, "no": schema}, Routes: []string{"yes", "no"}, DefaultRoute: "no", Predicate: "truthy", MaxAttempts: 1, TimeoutSeconds: 5}
	graph := base("draft", schema, []evolution.Node{router}, []evolution.Edge{{From: "$input.value", To: "route.value"}, {From: "route.yes", To: "$output.value"}})
	scope, err := DraftScope(graph, "node", "route", "scope")
	if err != nil {
		t.Fatal(err)
	}
	ref := add(c, scope)
	engine.Runtime = runtimeFunc(func(context.Context, Call) (Values, error) { t.Fatal("router used external runtime"); return nil, nil })
	result, err := engine.ExecuteDraftScope(context.Background(), ref, Values{"value": json.RawMessage(`true`)})
	if err != nil || string(result.Outputs["yes"]) != "true" || len(result.Outputs) != 1 || !reflect.DeepEqual(result.Inactive, []string{"no"}) || len(j.records) != 1 {
		t.Fatal(result, err)
	}
	if _, err = engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`true`)}); err == nil {
		t.Fatal("ordinary workflow relaxed required output")
	}
}

func TestDraftScopeDoesNotRelaxNestedRequiredOutputs(t *testing.T) {
	engine, c, _, schema := setup()
	router := evolution.Node{ID: "route", Kind: "router", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"yes": schema, "no": schema}, Routes: []string{"yes", "no"}, DefaultRoute: "no", Predicate: "truthy", MaxAttempts: 1, TimeoutSeconds: 5}
	body := add(c, base("body", schema, []evolution.Node{router}, []evolution.Edge{{From: "$input.value", To: "route.value"}, {From: "route.no", To: "$output.value"}}))
	node := evolution.Node{ID: "container", Kind: "subgraph", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"value": schema}, Body: &body, MaxDepth: 2, MaxAttempts: 1, TimeoutSeconds: 10}
	graph := base("draft", schema, []evolution.Node{node}, []evolution.Edge{{From: "$input.value", To: "container.value"}, {From: "container.value", To: "$output.value"}})
	scope, err := DraftScope(graph, "subgraph", "container", "scope")
	if err != nil {
		t.Fatal(err)
	}
	engine.Runtime = runtimeFunc(func(context.Context, Call) (Values, error) { t.Fatal("unexpected runtime"); return nil, nil })
	if _, err = engine.ExecuteDraftScope(context.Background(), add(c, scope), Values{"value": json.RawMessage(`true`)}); err == nil {
		t.Fatal("nested required output relaxed")
	}
}
