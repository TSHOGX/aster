// Package evolution defines immutable team evolution service contracts.
package evolution

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	manifest "aster/contracts/volumemanifest"
)

type Revision struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

func (r Revision) Validate() error {
	if !manifest.ValidID(r.ID) || r.Revision < 1 || !manifest.ValidDigest(r.Digest) {
		return fmt.Errorf("invalid immutable revision")
	}
	return nil
}

type Prompt struct {
	Text      string   `json:"text"`
	Variables []string `json:"variables"`
	Digest    string   `json:"digest"`
}
type Limits struct {
	MaxParallelNodes int `json:"max_parallel_nodes"`
	MaxTotalAttempts int `json:"max_total_attempts"`
	TimeoutSeconds   int `json:"timeout_seconds"`
}
type Node struct {
	ItemInputs        map[string]string `json:"item_inputs,omitempty"`
	ItemOutputs       map[string]string `json:"item_outputs,omitempty"`
	ID                string            `json:"id"`
	Kind              string            `json:"kind"`
	InputPorts        map[string]string `json:"input_ports"`
	OutputPorts       map[string]string `json:"output_ports"`
	Prompt            *Prompt           `json:"prompt,omitempty"`
	ModelRef          *Revision         `json:"model_ref,omitempty"`
	Tools             []Revision        `json:"tools,omitempty"`
	ImplementationRef *Revision         `json:"implementation_ref,omitempty"`
	Body              *Revision         `json:"body,omitempty"`
	MaxAttempts       int               `json:"max_attempts"`
	TimeoutSeconds    int               `json:"timeout_seconds"`
	MaxItems          int               `json:"max_items,omitempty"`
	Concurrency       int               `json:"concurrency,omitempty"`
	MaxIterations     int               `json:"max_iterations,omitempty"`
	MaxDepth          int               `json:"max_depth,omitempty"`
	Predicate         string            `json:"predicate,omitempty"`
	Routes            []string          `json:"routes,omitempty"`
	DefaultRoute      string            `json:"default_route,omitempty"`
	InactiveInputs    string            `json:"inactive_inputs,omitempty"`
	FailureMode       string            `json:"failure_mode,omitempty"`
	OnLimit           string            `json:"on_limit,omitempty"`
}
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type Workflow struct {
	SchemaVersion    int               `json:"schema_version"`
	ID               string            `json:"id"`
	ParametersSchema json.RawMessage   `json:"parameters_schema"`
	Inputs           map[string]string `json:"inputs"`
	Outputs          map[string]string `json:"outputs"`
	Nodes            []Node            `json:"nodes"`
	Edges            []Edge            `json:"edges"`
	Limits           Limits            `json:"limits"`
	Capabilities     []string          `json:"capabilities"`
}
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }
func Digest(b []byte) string       { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

var variablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

// Validate checks exact schema identities and DAG wiring, without accepting
// arbitrary executable predicates or silently dropping unsupported node fields.
func (w Workflow) Validate() error {
	fail := func(p, m string) error { return FieldError{p, m} }
	if w.SchemaVersion != 1 || !manifest.ValidID(w.ID) {
		return fail("id", "workflow-v1 and stable ID required")
	}
	if len(w.Nodes) == 0 || len(w.Nodes) > 1000 || w.Limits.MaxParallelNodes < 1 || w.Limits.MaxParallelNodes > 256 || w.Limits.MaxTotalAttempts < 1 || w.Limits.TimeoutSeconds < 1 {
		return fail("limits", "bounded nonempty workflow required")
	}
	var params map[string]any
	if json.Unmarshal(w.ParametersSchema, &params) != nil || params["type"] != "object" {
		return fail("parameters_schema", "object JSON schema required")
	}
	ports := func(p string, values map[string]string) error {
		for name, schema := range values {
			if !manifest.ValidID(name) || !manifest.ValidDigest(schema) {
				return fail(p+"."+name, "port requires stable name and exact schema digest")
			}
		}
		return nil
	}
	if err := ports("inputs", w.Inputs); err != nil {
		return err
	}
	if err := ports("outputs", w.Outputs); err != nil {
		return err
	}
	if len(w.Inputs) == 0 || len(w.Outputs) == 0 {
		return fail("ports", "input and output required")
	}
	nodes := map[string]Node{}
	sources := map[string]string{}
	targets := map[string]string{}
	for name, s := range w.Inputs {
		sources["$input."+name] = s
	}
	for name, s := range w.Outputs {
		targets["$output."+name] = s
	}
	for i, n := range w.Nodes {
		p := fmt.Sprintf("nodes[%d]", i)
		if !manifest.ValidID(n.ID) {
			return fail(p+".id", "invalid node ID")
		}
		if _, ok := nodes[n.ID]; ok {
			return fail(p+".id", "duplicate node")
		}
		nodes[n.ID] = n
		if n.MaxAttempts < 1 || n.MaxAttempts > w.Limits.MaxTotalAttempts || n.TimeoutSeconds < 1 || n.TimeoutSeconds > w.Limits.TimeoutSeconds {
			return fail(p, "bounded attempts and timeout required")
		}
		if err := ports(p+".input_ports", n.InputPorts); err != nil {
			return err
		}
		if err := ports(p+".output_ports", n.OutputPorts); err != nil {
			return err
		}
		for name, s := range n.InputPorts {
			targets[n.ID+"."+name] = s
		}
		for name, s := range n.OutputPorts {
			sources[n.ID+"."+name] = s
		}
		for _, r := range []*Revision{n.ModelRef, n.ImplementationRef, n.Body} {
			if r != nil {
				if err := r.Validate(); err != nil {
					return fail(p, err.Error())
				}
			}
		}
		for _, r := range n.Tools {
			if err := r.Validate(); err != nil {
				return fail(p+".tools", err.Error())
			}
		}
		switch n.Kind {
		case "agent":
			if n.Prompt == nil || n.ModelRef == nil {
				return fail(p, "agent requires prompt and model revision")
			}
			if n.Prompt.Digest != Digest([]byte(n.Prompt.Text)) {
				return fail(p+".prompt.digest", "prompt digest mismatch")
			}
			vars := map[string]bool{}
			for _, v := range n.Prompt.Variables {
				if vars[v] || n.InputPorts[v] == "" {
					return fail(p+".prompt.variables", "variables must bind unique input ports")
				}
				vars[v] = true
			}
			used := map[string]bool{}
			for _, m := range variablePattern.FindAllStringSubmatch(n.Prompt.Text, -1) {
				if !vars[m[1]] {
					return fail(p+".prompt.text", "unbound prompt variable")
				}
				used[m[1]] = true
			}
			if len(used) != len(vars) {
				return fail(p+".prompt.variables", "unused declared variable")
			}
		case "transform", "validator":
			if n.ImplementationRef == nil {
				return fail(p, "registered implementation required")
			}
		case "router":
			if !purePredicate(n.Predicate) || len(n.Routes) == 0 {
				return fail(p, "registered pure predicate and routes required")
			}
			seen := map[string]bool{}
			for _, route := range n.Routes {
				if n.OutputPorts[route] == "" || seen[route] {
					return fail(p+".routes", "invalid or duplicate route")
				}
				seen[route] = true
			}
			if !seen[n.DefaultRoute] {
				return fail(p+".default_route", "default must be declared route")
			}
		case "map":
			if len(n.ItemInputs) == 0 || len(n.ItemOutputs) == 0 {
				return fail(p, "map item port schemas required")
			}
			if err := ports(p+".item_inputs", n.ItemInputs); err != nil {
				return err
			}
			if err := ports(p+".item_outputs", n.ItemOutputs); err != nil {
				return err
			}
			if n.Body == nil || n.MaxItems < 1 || n.Concurrency < 1 || n.Concurrency > w.Limits.MaxParallelNodes || (n.FailureMode != "fail_fast" && n.FailureMode != "collect_errors") {
				return fail(p, "bounded body, concurrency and failure mode required")
			}
		case "join":
			if n.InactiveInputs != "skip" && n.InactiveInputs != "fail" {
				return fail(p, "inactive route handling required")
			}
		case "subgraph":
			if n.Body == nil || n.MaxDepth < 1 || n.MaxDepth > 16 {
				return fail(p, "bounded immutable subgraph required")
			}
		case "loop":
			if n.Body == nil || n.MaxIterations < 1 || !purePredicate(n.Predicate) || (n.OnLimit != "stop" && n.OnLimit != "fail") {
				return fail(p, "bounded loop and pure stop predicate required")
			}
		default:
			return fail(p+".kind", "unsupported node kind")
		}
	}
	incoming := map[string]string{}
	graph := map[string][]string{}
	degree := map[string]int{}
	for id := range nodes {
		degree[id] = 0
	}
	for i, e := range w.Edges {
		p := fmt.Sprintf("edges[%d]", i)
		s, ok := sources[e.From]
		if !ok {
			return fail(p+".from", "unknown source port")
		}
		target, ok := targets[e.To]
		if !ok || target != s {
			return fail(p+".to", "unknown or schema-incompatible target")
		}
		if _, ok := incoming[e.To]; ok {
			return fail(p+".to", "multiple producers require explicit join")
		}
		incoming[e.To] = e.From
		from, to := strings.SplitN(e.From, ".", 2)[0], strings.SplitN(e.To, ".", 2)[0]
		if from != "$input" && to != "$output" {
			graph[from] = append(graph[from], to)
			degree[to]++
		}
	}
	for target := range targets {
		if _, ok := incoming[target]; !ok {
			return fail(target, "required input or output is disconnected")
		}
	}
	queue := []string{}
	for id, d := range degree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range graph[id] {
			degree[next]--
			if degree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return fail("edges", "top-level cycles are forbidden")
	}
	// Trace every output back to an authorized input. Source-free islands cannot
	// masquerade as an executed graph with reachable required outputs.
	reachable := map[string]bool{}
	for name := range w.Inputs {
		reachable["$input."+name] = true
	}
	for pass := 0; pass <= len(nodes); pass++ {
		for target, source := range incoming {
			if reachable[source] {
				reachable[target] = true
			}
		}
		for _, node := range w.Nodes {
			connected := len(node.InputPorts) > 0
			for name := range node.InputPorts {
				if !reachable[node.ID+"."+name] {
					connected = false
				}
			}
			if connected {
				for name := range node.OutputPorts {
					reachable[node.ID+"."+name] = true
				}
			}
		}
	}
	for name := range w.Outputs {
		if !reachable["$output."+name] {
			return fail("outputs."+name, "output is not reachable from workflow inputs")
		}
	}
	return nil
}
func purePredicate(s string) bool { return s == "truthy" || s == "all_valid" || s == "has_items" }
func (w Workflow) Canonical() ([]byte, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	copy := w
	copy.Nodes = append([]Node(nil), w.Nodes...)
	copy.Edges = append([]Edge(nil), w.Edges...)
	copy.Capabilities = append([]string(nil), w.Capabilities...)
	sort.Slice(copy.Nodes, func(i, j int) bool { return copy.Nodes[i].ID < copy.Nodes[j].ID })
	sort.Slice(copy.Edges, func(i, j int) bool {
		a, b := copy.Edges[i], copy.Edges[j]
		return a.From < b.From || a.From == b.From && a.To < b.To
	})
	sort.Strings(copy.Capabilities)
	return json.Marshal(copy)
}
