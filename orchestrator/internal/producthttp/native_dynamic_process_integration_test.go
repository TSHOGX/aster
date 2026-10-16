//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
)

func publishNativeInspectionDefinition(t *testing.T, ctx context.Context, catalog, state, kind string, ref evolution.Revision, body any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": kind, "ref": ref, "body": body})
	must(t, err)
	command := exec.CommandContext(ctx, catalog, "--state-directory", state)
	command.Stdin = bytes.NewReader(raw)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dynamic publication: %v %s", err, output)
	}
}

func publishDynamicInspectionWorkflow(t *testing.T, ctx context.Context, catalog, state string, root evolution.Workflow, schemas map[string]json.RawMessage, model evolution.Revision) (evolution.Workflow, map[string]json.RawMessage) {
	t.Helper()
	array := root.Nodes[0].OutputPorts["items"]
	var schema map[string]any
	must(t, json.Unmarshal(schemas[array], &schema))
	item, err := json.Marshal(schema["items"])
	must(t, err)
	digest := evolution.Digest(item)
	ports := map[string]string{"items": digest}
	publish := func(graph evolution.Workflow) evolution.Revision {
		raw, err := graph.Canonical()
		must(t, err)
		ref := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(raw)}
		publishNativeInspectionDefinition(t, ctx, catalog, state, "workflow", ref, graph)
		return ref
	}
	prompt := "Review this exact proposal without changing its evidence: {{items}}"
	leaf := evolution.Workflow{SchemaVersion: 1, ID: "dynamic-item-agent", ParametersSchema: root.ParametersSchema, Inputs: ports, Outputs: ports, Nodes: []evolution.Node{{ID: "review-item", Kind: "agent", InputPorts: ports, OutputPorts: ports, ModelRef: &model, Prompt: &evolution.Prompt{Text: prompt, Variables: []string{"items"}, Digest: evolution.Digest([]byte(prompt))}, MaxAttempts: 1, TimeoutSeconds: 30}}, Edges: []evolution.Edge{{From: "$input.items", To: "review-item.items"}, {From: "review-item.items", To: "$output.items"}}, Limits: evolution.Limits{MaxParallelNodes: 1, MaxTotalAttempts: 1, TimeoutSeconds: 60}}
	leafRef := publish(leaf)
	container := leaf
	container.ID = "dynamic-item-container"
	container.Nodes = []evolution.Node{{ID: "sub", Kind: "subgraph", InputPorts: ports, OutputPorts: ports, Body: &leafRef, MaxDepth: 4, MaxAttempts: 1, TimeoutSeconds: 60}}
	container.Edges = []evolution.Edge{{From: "$input.items", To: "sub.items"}, {From: "sub.items", To: "$output.items"}}
	container.Limits.MaxTotalAttempts = 2
	containerRef := publish(container)
	root.Nodes = append(root.Nodes, evolution.Node{ID: "review-map", Kind: "map", InputPorts: map[string]string{"items": array}, OutputPorts: map[string]string{"items": array}, ItemInputs: ports, ItemOutputs: ports, Body: &containerRef, MaxItems: 4, Concurrency: 2, FailureMode: "fail_fast", MaxAttempts: 1, TimeoutSeconds: 90})
	for i := range root.Edges {
		if root.Edges[i].From == "extract.items" {
			root.Edges[i].From = "review-map.items"
		}
	}
	root.Edges = append(root.Edges, evolution.Edge{From: "extract.items", To: "review-map.items"})
	return root, map[string]json.RawMessage{digest: item}
}

type dynamicAttempt struct {
	Call struct {
		ID, Instance, Parent string
		Number               int
		Node                 struct{ ID, Kind string }
		Inputs               map[string]json.RawMessage
		Prompt               string
	}
	State   string
	Outputs map[string]json.RawMessage
}

func verifyNativeDynamicInspection(t *testing.T, ctx context.Context, fixture nativeToolInspection) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+fixture.State+"/projections.db?mode=ro")
	must(t, err)
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT record FROM workflow_attempts WHERE run_id=?", fixture.Run)
	must(t, err)
	attempts := map[string]dynamicAttempt{}
	leaves := []dynamicAttempt{}
	for rows.Next() {
		var raw []byte
		must(t, rows.Scan(&raw))
		var attempt dynamicAttempt
		must(t, json.Unmarshal(raw, &attempt))
		attempts[attempt.Call.ID] = attempt
		if attempt.Call.Node.ID == "review-item" {
			leaves = append(leaves, attempt)
		}
	}
	must(t, rows.Err())
	must(t, rows.Close())
	if len(leaves) != 2 {
		t.Fatal("actual map instances missing", len(leaves))
	}
	for _, leaf := range leaves {
		parent := attempts[leaf.Call.Parent]
		grandparent := attempts[parent.Call.Parent]
		if leaf.State != "succeeded" || parent.Call.Node.Kind != "subgraph" || grandparent.Call.Node.Kind != "map" || leaf.Call.Number != 1 || !bytes.Equal(leaf.Call.Inputs["items"], leaf.Outputs["items"]) || !strings.Contains(leaf.Call.Instance, "/item-") || !strings.Contains(leaf.Call.Instance, "/body/") {
			t.Fatal("dynamic lineage/evidence mismatch", leaf, parent, grandparent)
		}
	}
	_, err = fixture.Store.SetEvolutionGroupGrant(ctx, fixture.Token, fixture.Group, product.EvolutionGroupGrantInput{RequestID: "dynamic-inspector", AccountID: fixture.Actor, Action: "source.read", Effect: "allow"})
	must(t, err)
	if !fixture.Browser {
		return
	}
	browser := os.Getenv("EVALUATION_TEST_BROWSER")
	if browser == "" {
		browser = "chromium"
	}
	width := 1440
	if os.Getenv("EVALUATION_NARROW") == "1" {
		width = 390
	}
	config, _ := json.Marshal(map[string]any{"origin": fixture.Origin, "cookie": fixture.Token, "group": fixture.Group, "run": fixture.Run, "browser": browser, "width": width, "evidence_directory": filepath.Join(fixture.Repository, "evaluation-space/frontend/test-results/dynamic", browser)})
	command := exec.CommandContext(ctx, "node", filepath.Join(fixture.Repository, "evaluation-space/tests/browser/dynamic.mjs"))
	command.Stdin = bytes.NewReader(config)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dynamic browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}
