package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aster/contracts/evolution"
)

type Engine struct {
	Catalog Catalog
	Runtime Runtime
	Journal Journal
}
type execution struct {
	engine            *Engine
	compiled          *compiled
	maxAttempts       int
	slots             chan struct{}
	allowInactiveRoot bool
	inactiveRoot      []string
}
type nodeResult struct {
	id       string
	values   Values
	inactive map[string]bool
	err      error
}

// DraftScopeResult preserves inactive output identity without inventing values.
type DraftScopeResult struct {
	Outputs  Values   `json:"outputs"`
	Inactive []string `json:"inactive"`
}

func (e *Engine) Execute(ctx context.Context, ref evolution.Revision, inputs Values) (Values, error) {
	result, err := e.execute(ctx, ref, inputs, false)
	return result.Outputs, err
}

// ExecuteDraftScope permits inactive outputs only at the partial test root.
// Nested graphs retain their required-output semantics.
func (e *Engine) ExecuteDraftScope(ctx context.Context, ref evolution.Revision, inputs Values) (DraftScopeResult, error) {
	return e.execute(ctx, ref, inputs, true)
}

func (e *Engine) execute(ctx context.Context, ref evolution.Revision, inputs Values, partial bool) (DraftScopeResult, error) {
	if e.Catalog == nil || e.Runtime == nil || e.Journal == nil {
		return DraftScopeResult{}, fmt.Errorf("workflow dependencies required")
	}
	compiled, err := compile(ctx, ref, e.Catalog)
	if err != nil {
		return DraftScopeResult{}, err
	}
	root := compiled.workflows[ref]
	ctx, cancel := context.WithTimeout(ctx, time.Duration(root.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	execution := &execution{engine: e, compiled: compiled, maxAttempts: root.Limits.MaxTotalAttempts, slots: make(chan struct{}, root.Limits.MaxParallelNodes), allowInactiveRoot: partial, inactiveRoot: []string{}}
	outputs, err := execution.graph(ctx, ref, inputs, "root", "", 0)
	return DraftScopeResult{Outputs: outputs, Inactive: execution.inactiveRoot}, err
}
func (x *execution) graph(ctx context.Context, ref evolution.Revision, inputs Values, instance, parent string, depth int) (Values, error) {
	if depth > 16 {
		return nil, fmt.Errorf("workflow depth exceeded")
	}
	w := x.compiled.workflows[ref]
	if err := x.compiled.validate(w.Inputs, inputs, nil); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(w.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	values := map[string]json.RawMessage{}
	inactive := map[string]bool{}
	for name, value := range inputs {
		values["$input."+name] = bytes.Clone(value)
	}
	incoming := map[string]string{}
	for _, edge := range w.Edges {
		incoming[edge.To] = edge.From
	}
	remaining := map[string]evolution.Node{}
	for _, node := range w.Nodes {
		remaining[node.ID] = node
	}
	running := 0
	results := make(chan nodeResult, len(w.Nodes))
	var firstErr error
	for len(remaining) > 0 || running > 0 {
		if firstErr == nil && ctx.Err() != nil {
			firstErr = ctx.Err()
			cancel()
		}
		if firstErr == nil {
			for _, id := range keys(remaining) {
				if running >= w.Limits.MaxParallelNodes {
					break
				}
				node := remaining[id]
				ready := true
				nodeInputs := Values{}
				missing := []string{}
				for name := range node.InputPorts {
					source := incoming[id+"."+name]
					if inactive[source] {
						missing = append(missing, name)
					} else if value, ok := values[source]; ok {
						nodeInputs[name] = bytes.Clone(value)
					} else {
						ready = false
					}
				}
				if !ready {
					continue
				}
				delete(remaining, id)
				running++
				sort.Strings(missing)
				go func(node evolution.Node, input Values, missing []string) {
					out, off, err := x.node(ctx, ref, node, input, missing, instance+"/"+node.ID, parent, depth)
					results <- nodeResult{node.ID, out, off, err}
				}(node, nodeInputs, missing)
			}
		}
		if running == 0 {
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, fmt.Errorf("unresolvable workflow dependencies")
		}
		result := <-results
		running--
		if result.err != nil {
			if firstErr == nil || errors.Is(firstErr, ErrPauseRequested) && !errors.Is(result.err, ErrPauseRequested) {
				firstErr = result.err
				// A pause stops scheduling while already admitted siblings settle.
				// Unknown/failed effects still take precedence over a pause boundary.
				if !errors.Is(result.err, ErrPauseRequested) {
					cancel()
				}
			}
			continue
		}
		for port, value := range result.values {
			values[result.id+"."+port] = value
		}
		for port := range result.inactive {
			inactive[result.id+"."+port] = true
		}
		if firstErr != nil && running == 0 {
			return nil, firstErr
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	outputs := Values{}
	off := map[string]bool{}
	for name := range w.Outputs {
		source := incoming["$output."+name]
		if inactive[source] {
			if x.allowInactiveRoot && instance == "root" {
				off[name] = true
				x.inactiveRoot = append(x.inactiveRoot, name)
				continue
			}
			return nil, NodeFailure{instance, "required_output_inactive"}
		}
		outputs[name] = values[source]
	}
	if instance == "root" {
		sort.Strings(x.inactiveRoot)
	}
	if err := x.compiled.validate(w.Outputs, outputs, off); err != nil {
		return nil, err
	}
	return outputs, nil
}

var promptVariable = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

func effectivePrompt(node evolution.Node, inputs Values) string {
	if node.Prompt == nil {
		return ""
	}
	return promptVariable.ReplaceAllStringFunc(node.Prompt.Text, func(token string) string {
		name := promptVariable.FindStringSubmatch(token)[1]
		var value string
		if json.Unmarshal(inputs[name], &value) == nil {
			return value
		}
		return string(inputs[name])
	})
}
func (x *execution) node(ctx context.Context, ref evolution.Revision, node evolution.Node, inputs Values, missing []string, instance, parent string, depth int) (Values, map[string]bool, error) {
	skip := len(missing) > 0 && node.Kind != "join"
	if node.Kind == "join" && len(missing) > 0 && node.InactiveInputs == "fail" {
		return nil, nil, NodeFailure{instance, "inactive_join_input"}
	}
	if node.Kind == "join" && len(inputs) == 0 {
		skip = true
	}
	for number := 1; number <= node.MaxAttempts; number++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		call := Call{ID: strings.TrimPrefix(evolution.Digest([]byte(instance+fmt.Sprint("/", number))), "sha256:"), Instance: instance, Parent: parent, Number: number, Workflow: ref, Node: node, Inputs: inputs, Prompt: effectivePrompt(node, inputs)}
		record, err := x.engine.Journal.Begin(ctx, call, x.maxAttempts)
		if err != nil {
			return nil, nil, err
		}
		if record.State == "succeeded" || record.State == "skipped" {
			off := map[string]bool{}
			for _, name := range record.Inactive {
				off[name] = true
			}
			if err = x.compiled.validate(node.OutputPorts, record.Outputs, off); err != nil {
				return nil, nil, err
			}
			return record.Outputs, off, nil
		}
		if record.State == "failed" {
			if record.ErrorCode == "retryable_failure" && number < node.MaxAttempts {
				continue
			}
			return nil, nil, NodeFailure{instance, record.ErrorCode}
		}
		record.State = "succeeded"
		record.Outputs = Values{}
		off := map[string]bool{}
		if skip {
			record.State = "skipped"
			record.ErrorCode = "inactive_route"
			for name := range node.OutputPorts {
				off[name] = true
			}
		} else {
			nodeCtx, cancel := context.WithTimeout(ctx, time.Duration(node.TimeoutSeconds)*time.Second)
			record.Outputs, off, err = x.evaluate(nodeCtx, call, depth)
			cancel()
			if err == nil {
				if validationErr := x.compiled.validate(node.OutputPorts, record.Outputs, off); validationErr != nil {
					err = Failure{Code: "invalid_node_output"}
				}
			}
			if err != nil {
				var failure Failure
				var nested NodeFailure
				if !errors.As(err, &failure) && !errors.As(err, &nested) {
					return nil, nil, err
				}
				record.State = "failed"
				record.Outputs = Values{}
				record.ErrorCode = "node_failed"
				if errors.As(err, &nested) {
					record.ErrorCode = nested.Code
				}
				if failure.Retryable {
					record.ErrorCode = "retryable_failure"
				} else if failure.Code != "" {
					record.ErrorCode = failure.Code
				}
			}
		}
		for name := range off {
			record.Inactive = append(record.Inactive, name)
		}
		sort.Strings(record.Inactive)
		if commitErr := x.engine.Journal.Complete(ctx, record); commitErr != nil {
			return nil, nil, commitErr
		}
		if record.State == "failed" {
			if record.ErrorCode == "retryable_failure" && number < node.MaxAttempts {
				continue
			}
			return nil, nil, NodeFailure{instance, record.ErrorCode}
		}
		return record.Outputs, off, nil
	}
	return nil, nil, ErrAttemptLimit
}
func (x *execution) evaluate(ctx context.Context, call Call, depth int) (Values, map[string]bool, error) {
	node := call.Node
	switch node.Kind {
	case "agent", "transform", "validator":
		select {
		case x.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		defer func() { <-x.slots }()
		// Registered adapters receive a detached request. A transform must not
		// mutate the journaled call or the compiled graph through shared map slices.
		raw, err := json.Marshal(call)
		if err != nil {
			return nil, nil, err
		}
		detached, err := evolution.Decode[Call](bytes.NewReader(raw))
		if err != nil {
			return nil, nil, err
		}
		out, err := x.engine.Runtime.Execute(ctx, detached)
		if err != nil {
			return nil, nil, err
		}
		output, err := json.Marshal(out)
		if err != nil {
			return nil, nil, Failure{Code: "invalid_node_output"}
		}
		copied, err := evolution.Decode[Values](bytes.NewReader(output))
		if err != nil {
			return nil, nil, Failure{Code: "invalid_node_output"}
		}
		return copied, nil, nil
	case "router":
		raw := call.Inputs[keys(call.Inputs)[0]]
		selected := node.DefaultRoute
		if predicate(node.Predicate, raw) {
			for _, route := range node.Routes {
				if route != node.DefaultRoute {
					selected = route
					break
				}
			}
		}
		off := map[string]bool{}
		for name := range node.OutputPorts {
			if name != selected {
				off[name] = true
			}
		}
		return Values{selected: raw}, off, nil
	case "join":
		raw, _ := json.Marshal(call.Inputs)
		return Values{keys(node.OutputPorts)[0]: raw}, nil, nil
	case "subgraph":
		out, err := x.graph(ctx, *node.Body, call.Inputs, fmt.Sprintf("%s/attempt-%d/body", call.Instance, call.Number), call.ID, depth+1)
		return out, nil, err
	case "loop":
		current := call.Inputs
		for iteration := 0; iteration < node.MaxIterations; iteration++ {
			out, err := x.graph(ctx, *node.Body, current, fmt.Sprintf("%s/attempt-%d/iteration-%d", call.Instance, call.Number, iteration), call.ID, depth+1)
			if err != nil {
				return nil, nil, err
			}
			current = out
			raw, _ := json.Marshal(out)
			if len(out) == 1 {
				raw = out[keys(out)[0]]
			}
			if predicate(node.Predicate, raw) {
				return out, nil, nil
			}
		}
		if node.OnLimit == "fail" {
			return nil, nil, Failure{Code: "loop_limit"}
		}
		return current, nil, nil
	case "map":
		return x.mapNode(ctx, call, depth)
	}
	return nil, nil, Failure{Code: "unsupported_node"}
}
func predicate(name string, raw json.RawMessage) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	switch name {
	case "truthy":
		switch v := value.(type) {
		case bool:
			return v
		case string:
			return v != ""
		case json.Number:
			number, err := v.Float64()
			return err == nil && number != 0
		case []any:
			return len(v) > 0
		case map[string]any:
			return len(v) > 0
		}
	case "has_items":
		v, ok := value.([]any)
		return ok && len(v) > 0
	case "all_valid":
		valid := func(value any) bool {
			if b, ok := value.(bool); ok {
				return b
			}
			if obj, ok := value.(map[string]any); ok {
				b, _ := obj["valid"].(bool)
				return b
			}
			return false
		}
		if items, ok := value.([]any); ok {
			if len(items) == 0 {
				return false
			}
			for _, item := range items {
				if !valid(item) {
					return false
				}
			}
			return true
		}
		return valid(value)
	}
	return false
}
func (x *execution) mapNode(ctx context.Context, call Call, depth int) (Values, map[string]bool, error) {
	node := call.Node
	arrays := map[string][]json.RawMessage{}
	count := -1
	for name, raw := range call.Inputs {
		var items []json.RawMessage
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &items) != nil || len(items) > node.MaxItems {
			return nil, nil, Failure{Code: "map_input_limit"}
		}
		if count >= 0 && count != len(items) {
			return nil, nil, Failure{Code: "map_input_lengths"}
		}
		count = len(items)
		arrays[name] = items
	}
	if count < 0 {
		return nil, nil, Failure{Code: "map_input_missing"}
	}
	outputs := make([]Values, count)
	failures := make([]error, count)
	sem := make(chan struct{}, node.Concurrency)
	var workers sync.WaitGroup
	var pausing atomic.Bool
	work, cancel := context.WithCancel(ctx)
	defer cancel()
scheduling:
	for i := 0; i < count; i++ {
		select {
		case sem <- struct{}{}:
		case <-work.Done():
			break scheduling
		}
		if pausing.Load() {
			<-sem
			break scheduling
		}
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			defer func() { <-sem }()
			input := Values{}
			for name, items := range arrays {
				input[name] = items[i]
			}
			outputs[i], failures[i] = x.graph(work, *node.Body, input, fmt.Sprintf("%s/attempt-%d/item-%d", call.Instance, call.Number, i), call.ID, depth+1)
			if errors.Is(failures[i], ErrPauseRequested) {
				pausing.Store(true)
			} else if failures[i] != nil && node.FailureMode == "fail_fast" {
				cancel()
			}
		}(i)
	}
	workers.Wait()
	if node.FailureMode == "fail_fast" {
		for _, err := range failures {
			var terminal NodeFailure
			if errors.As(err, &terminal) {
				return nil, nil, terminal
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if pausing.Load() {
		for _, err := range failures {
			if err != nil && !errors.Is(err, ErrPauseRequested) {
				return nil, nil, err
			}
		}
		return nil, nil, ErrPauseRequested
	}
	if node.FailureMode == "fail_fast" && work.Err() != nil {
		return nil, nil, work.Err()
	}
	out := Values{}
	for _, name := range keys(node.OutputPorts) {
		items := make([]json.RawMessage, count)
		for i := range outputs {
			if failures[i] != nil {
				var terminal NodeFailure
				if !errors.As(failures[i], &terminal) || node.FailureMode == "fail_fast" {
					return nil, nil, failures[i]
				}
				items[i], _ = json.Marshal(map[string]any{"state": "failed", "error_code": terminal.Code})
			} else if node.FailureMode == "collect_errors" {
				items[i], _ = json.Marshal(map[string]any{"state": "succeeded", "value": outputs[i][name]})
			} else {
				items[i] = outputs[i][name]
			}
		}
		out[name], _ = json.Marshal(items)
	}
	return out, nil, nil
}
