package evolution

import (
	"encoding/json"
	"testing"
)

func simpleWorkflow() Workflow {
	schema := Digest([]byte("artifact-list-v1"))
	patch := Digest([]byte("volume-patch-v1"))
	rev := Revision{"native", 1, Digest([]byte("native-v1"))}
	prompt := Prompt{Text: "Extract {{corpus}}", Variables: []string{"corpus"}}
	prompt.Digest = Digest([]byte(prompt.Text))
	return Workflow{SchemaVersion: 1, ID: "corrections", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: map[string]string{"corpus": schema}, Outputs: map[string]string{"patch": patch}, Nodes: []Node{{ID: "extract", Kind: "agent", InputPorts: map[string]string{"corpus": schema}, OutputPorts: map[string]string{"items": schema}, Prompt: &prompt, ModelRef: &rev, MaxAttempts: 2, TimeoutSeconds: 120}, {ID: "build", Kind: "transform", InputPorts: map[string]string{"items": schema}, OutputPorts: map[string]string{"patch": patch}, ImplementationRef: &rev, MaxAttempts: 1, TimeoutSeconds: 120}}, Edges: []Edge{{"$input.corpus", "extract.corpus"}, {"extract.items", "build.items"}, {"build.patch", "$output.patch"}}, Limits: Limits{4, 20, 900}, Capabilities: []string{}}
}
func TestWorkflowValidation(t *testing.T) {
	if err := simpleWorkflow().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Workflow){"unsupported": func(w *Workflow) { w.Nodes[0].Kind = "shell" }, "unbounded": func(w *Workflow) { w.Nodes[0].Kind = "loop" }, "dangling": func(w *Workflow) { w.Edges[0].From = "missing.port" }, "schema": func(w *Workflow) { w.Nodes[0].InputPorts["corpus"] = Digest([]byte("other")) }, "duplicate": func(w *Workflow) { w.Nodes[1].ID = "extract" }, "unbound_prompt": func(w *Workflow) {
		w.Nodes[0].Prompt.Text = "{{secret}}"
		w.Nodes[0].Prompt.Digest = Digest([]byte("{{secret}}"))
	}, "disconnected": func(w *Workflow) { w.Edges = w.Edges[:2] }, "multiple_writers": func(w *Workflow) { w.Edges = append(w.Edges, w.Edges[0]) }, "cycle": func(w *Workflow) {
		w.Nodes[1].OutputPorts["items"] = w.Inputs["corpus"]
		w.Edges[0].From = "build.items"
	}}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			w := simpleWorkflow()
			change(&w)
			if w.Validate() == nil {
				t.Fatal("invalid workflow accepted")
			}
		})
	}
}
func TestLayoutIndependentCanonicalOrder(t *testing.T) {
	a := simpleWorkflow()
	b := simpleWorkflow()
	b.Nodes[0], b.Nodes[1] = b.Nodes[1], b.Nodes[0]
	b.Edges[0], b.Edges[1] = b.Edges[1], b.Edges[0]
	left, e := a.Canonical()
	if e != nil {
		t.Fatal(e)
	}
	right, e := b.Canonical()
	if e != nil || string(left) != string(right) {
		t.Fatal("ordering changed executable identity")
	}
}
