package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"aster/contracts/evolution"
)

type testCatalog struct {
	graphs  map[evolution.Revision]evolution.Workflow
	schemas map[string]json.RawMessage
}

func (c testCatalog) ResolveWorkflow(_ context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	w, ok := c.graphs[ref]
	if !ok {
		return w, fmt.Errorf("missing graph")
	}
	return w, nil
}
func (c testCatalog) Schema(_ context.Context, digest string) (json.RawMessage, error) {
	raw, ok := c.schemas[digest]
	if !ok {
		return nil, fmt.Errorf("missing schema")
	}
	return raw, nil
}
func (c testCatalog) Check(context.Context, evolution.Node) error { return nil }

type runtimeFunc func(context.Context, Call) (Values, error)

func (f runtimeFunc) Execute(ctx context.Context, call Call) (Values, error) { return f(ctx, call) }

type memoryJournal struct {
	mu      sync.Mutex
	records map[string]Record
}

func (j *memoryJournal) Begin(_ context.Context, call Call, max int) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if record, ok := j.records[call.ID]; ok {
		if !reflect.DeepEqual(record.Call, call) {
			return Record{}, fmt.Errorf("rebound attempt")
		}
		return record, nil
	}
	if len(j.records) >= max {
		return Record{}, ErrAttemptLimit
	}
	record := Record{Call: call, State: "running"}
	j.records[call.ID] = record
	return record, nil
}
func (j *memoryJournal) Complete(_ context.Context, record Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records[record.Call.ID] = record
	return nil
}
func setup() (*Engine, *testCatalog, *memoryJournal, string) {
	c := &testCatalog{graphs: map[evolution.Revision]evolution.Workflow{}, schemas: map[string]json.RawMessage{}}
	raw := json.RawMessage(`{}`)
	digest := evolution.Digest(raw)
	c.schemas[digest] = raw
	j := &memoryJournal{records: map[string]Record{}}
	return &Engine{Catalog: c, Journal: j}, c, j, digest
}
func add(c *testCatalog, w evolution.Workflow) evolution.Revision {
	raw, err := w.Canonical()
	if err != nil {
		panic(err)
	}
	ref := evolution.Revision{ID: w.ID, Revision: 1, Digest: evolution.Digest(raw)}
	c.graphs[ref] = w
	return ref
}
func base(id, schema string, nodes []evolution.Node, edges []evolution.Edge) evolution.Workflow {
	return evolution.Workflow{SchemaVersion: 1, ID: id, ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: map[string]string{"value": schema}, Outputs: map[string]string{"value": schema}, Nodes: nodes, Edges: edges, Limits: evolution.Limits{MaxParallelNodes: 3, MaxTotalAttempts: 100, TimeoutSeconds: 30}}
}
func transform(id, schema string) evolution.Node {
	return evolution.Node{ID: id, Kind: "transform", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"value": schema}, ImplementationRef: &evolution.Revision{ID: "identity", Revision: 1, Digest: evolution.Digest([]byte("identity"))}, MaxAttempts: 2, TimeoutSeconds: 5}
}
func TestGraphAttemptsRecoverAndRenderActualPrompts(t *testing.T) {
	engine, c, j, schema := setup()
	node := transform("agent", schema)
	node.Kind = "agent"
	node.ImplementationRef = nil
	node.ModelRef = &evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	text := "Read {{ value }}"
	node.Prompt = &evolution.Prompt{Text: text, Variables: []string{"value"}, Digest: evolution.Digest([]byte(text))}
	ref := add(c, base("graph", schema, []evolution.Node{node, transform("build", schema)}, []evolution.Edge{{From: "$input.value", To: "agent.value"}, {From: "agent.value", To: "build.value"}, {From: "build.value", To: "$output.value"}}))
	calls := map[string]int{}
	lost := true
	engine.Runtime = runtimeFunc(func(_ context.Context, call Call) (Values, error) {
		calls[call.Node.ID]++
		if call.Node.ID == "agent" && call.Prompt != "Read corpus" {
			t.Fatal(call.Prompt)
		}
		if call.Node.ID == "build" && lost {
			return nil, ErrUncertain
		}
		return call.Inputs, nil
	})
	if _, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`"corpus"`)}); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	lost = false
	out, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`"corpus"`)})
	if err != nil || string(out["value"]) != `"corpus"` {
		t.Fatal(out, err)
	}
	if calls["agent"] != 1 || calls["build"] != 2 || len(j.records) != 2 {
		t.Fatal("recovery changed attempt identity", calls, j.records)
	}
}
func TestGraphRouterSkipsInactiveBranchAndJoin(t *testing.T) {
	engine, c, j, schema := setup()
	router := evolution.Node{ID: "route", Kind: "router", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"yes": schema, "no": schema}, Routes: []string{"yes", "no"}, DefaultRoute: "no", Predicate: "truthy", MaxAttempts: 1, TimeoutSeconds: 5}
	join := evolution.Node{ID: "join", Kind: "join", InputPorts: map[string]string{"yes": schema, "no": schema}, OutputPorts: map[string]string{"value": schema}, InactiveInputs: "skip", MaxAttempts: 1, TimeoutSeconds: 5}
	ref := add(c, base("routes", schema, []evolution.Node{router, transform("yes", schema), transform("no", schema), join}, []evolution.Edge{{From: "$input.value", To: "route.value"}, {From: "route.yes", To: "yes.value"}, {From: "route.no", To: "no.value"}, {From: "yes.value", To: "join.yes"}, {From: "no.value", To: "join.no"}, {From: "join.value", To: "$output.value"}}))
	engine.Runtime = runtimeFunc(func(_ context.Context, call Call) (Values, error) {
		if call.Node.ID != "yes" {
			t.Fatal("inactive node executed")
		}
		return call.Inputs, nil
	})
	out, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`true`)})
	if err != nil || string(out["value"]) != `{"yes":true}` {
		t.Fatal(out, err)
	}
	skipped := 0
	for _, record := range j.records {
		if record.State == "skipped" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatal("missing skipped observation")
	}
}
func TestMapBoundsConcurrencyOrdersOutputsAndCollectsFailures(t *testing.T) {
	for _, mode := range []string{"fail_fast", "collect_errors"} {
		t.Run(mode, func(t *testing.T) {
			engine, c, _, schema := setup()
			body := add(c, base("body", schema, []evolution.Node{transform("work", schema)}, []evolution.Edge{{From: "$input.value", To: "work.value"}, {From: "work.value", To: "$output.value"}}))
			node := evolution.Node{ID: "map", Kind: "map", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"value": schema}, ItemInputs: map[string]string{"value": schema}, ItemOutputs: map[string]string{"value": schema}, Body: &body, MaxItems: 10, Concurrency: 2, FailureMode: mode, MaxAttempts: 1, TimeoutSeconds: 10}
			ref := add(c, base("map", schema, []evolution.Node{node}, []evolution.Edge{{From: "$input.value", To: "map.value"}, {From: "map.value", To: "$output.value"}}))
			var active, peak, arrivals atomic.Int64
			firstPair := make(chan struct{})
			engine.Runtime = runtimeFunc(func(ctx context.Context, call Call) (Values, error) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				// Require two calls to overlap explicitly. A short sleep cannot
				// prove concurrency when the test host schedules one call late.
				if arrivals.Add(1) == 2 {
					close(firstPair)
				}
				select {
				case <-firstPair:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if mode == "collect_errors" && string(call.Inputs["value"]) == "2" {
					return nil, Failure{Code: "unsupported_fact"}
				}
				return call.Inputs, nil
			})
			out, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`[3,2,1]`)})
			if err != nil {
				t.Fatal(err)
			}
			if peak.Load() > 2 || peak.Load() < 2 {
				t.Fatal("map concurrency", peak.Load())
			}
			expected := `[3,2,1]`
			if mode == "collect_errors" {
				expected = `[{"state":"succeeded","value":3},{"error_code":"unsupported_fact","state":"failed"},{"state":"succeeded","value":1}]`
			}
			if string(out["value"]) != expected {
				t.Fatal(string(out["value"]))
			}
		})
	}
}
func TestLoopAndSubgraphExposeBoundedIterations(t *testing.T) {
	engine, c, j, schema := setup()
	body := add(c, base("body", schema, []evolution.Node{transform("increment", schema)}, []evolution.Edge{{From: "$input.value", To: "increment.value"}, {From: "increment.value", To: "$output.value"}}))
	loop := evolution.Node{ID: "loop", Kind: "loop", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"value": schema}, Body: &body, Predicate: "truthy", MaxIterations: 2, OnLimit: "fail", MaxAttempts: 1, TimeoutSeconds: 10}
	inner := add(c, base("inner", schema, []evolution.Node{loop}, []evolution.Edge{{From: "$input.value", To: "loop.value"}, {From: "loop.value", To: "$output.value"}}))
	sub := evolution.Node{ID: "sub", Kind: "subgraph", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"value": schema}, Body: &inner, MaxDepth: 4, MaxAttempts: 1, TimeoutSeconds: 15}
	ref := add(c, base("root", schema, []evolution.Node{sub}, []evolution.Edge{{From: "$input.value", To: "sub.value"}, {From: "sub.value", To: "$output.value"}}))
	count := 0
	engine.Runtime = runtimeFunc(func(_ context.Context, call Call) (Values, error) {
		count++
		return Values{"value": json.RawMessage(fmt.Sprint(count == 2))}, nil
	})
	out, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`false`)})
	if err != nil || string(out["value"]) != "true" || count != 2 || len(j.records) != 4 {
		t.Fatal(out, err, count, len(j.records))
	}
}
func TestInvalidOutputAndKnownRetryAreDurable(t *testing.T) {
	engine, c, j, _ := setup()
	raw := json.RawMessage(`{"type":"string"}`)
	schema := evolution.Digest(raw)
	c.schemas[schema] = raw
	ref := add(c, base("root", schema, []evolution.Node{transform("work", schema)}, []evolution.Edge{{From: "$input.value", To: "work.value"}, {From: "work.value", To: "$output.value"}}))
	calls := 0
	engine.Runtime = runtimeFunc(func(_ context.Context, call Call) (Values, error) {
		calls++
		if calls == 1 {
			return nil, Failure{Code: "retry", Retryable: true}
		}
		return Values{"value": json.RawMessage(`12`)}, nil
	})
	_, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`"input"`)})
	var failed NodeFailure
	if !errors.As(err, &failed) || failed.Code != "invalid_node_output" || len(j.records) != 2 {
		t.Fatal(err, j.records)
	}
	_, _ = engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`"input"`)})
	if calls != 2 {
		t.Fatal("terminal failure reran", calls)
	}
}

func TestMapFailureAndAttemptLimitNeverFabricateResults(t *testing.T) {
	for _, mode := range []string{"terminal", "uncertain", "budget"} {
		t.Run(mode, func(t *testing.T) {
			engine, c, _, schema := setup()
			body := add(c, base("body", schema, []evolution.Node{transform("work", schema)}, []evolution.Edge{{From: "$input.value", To: "work.value"}, {From: "work.value", To: "$output.value"}}))
			node := evolution.Node{ID: "map", Kind: "map", InputPorts: map[string]string{"value": schema}, OutputPorts: map[string]string{"value": schema}, ItemInputs: map[string]string{"value": schema}, ItemOutputs: map[string]string{"value": schema}, Body: &body, MaxItems: 10, Concurrency: 2, FailureMode: "fail_fast", MaxAttempts: 1, TimeoutSeconds: 5}
			w := base("root", schema, []evolution.Node{node}, []evolution.Edge{{From: "$input.value", To: "map.value"}, {From: "map.value", To: "$output.value"}})
			if mode == "budget" {
				w.Limits.MaxTotalAttempts = 2
			}
			ref := add(c, w)
			engine.Runtime = runtimeFunc(func(context.Context, Call) (Values, error) {
				if mode == "terminal" {
					return nil, Failure{Code: "known_failure"}
				}
				if mode == "uncertain" {
					return nil, ErrUncertain
				}
				return Values{"value": json.RawMessage(`true`)}, nil
			})
			out, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`[1,2,3,4]`)})
			if err == nil || out != nil {
				t.Fatal("failed map returned success", out, err)
			}
			if mode == "terminal" {
				var failure NodeFailure
				if !errors.As(err, &failure) || failure.Code != "known_failure" {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRuntimeCannotMutateFrozenGraphOrAttemptInputs(t *testing.T) {
	engine, c, j, schema := setup()
	ref := add(c, base("graph", schema, []evolution.Node{transform("work", schema)}, []evolution.Edge{{From: "$input.value", To: "work.value"}, {From: "work.value", To: "$output.value"}}))
	engine.Runtime = runtimeFunc(func(_ context.Context, call Call) (Values, error) {
		call.Inputs["value"] = json.RawMessage(`"changed"`)
		delete(call.Node.OutputPorts, "value")
		return Values{"value": json.RawMessage(`"output"`)}, nil
	})
	out, err := engine.Execute(context.Background(), ref, Values{"value": json.RawMessage(`"original"`)})
	if err != nil || string(out["value"]) != `"output"` {
		t.Fatal(out, err)
	}
	for _, record := range j.records {
		if string(record.Call.Inputs["value"]) != `"original"` || record.Call.Node.OutputPorts["value"] != schema {
			t.Fatal("adapter mutated provenance", record)
		}
	}
}
