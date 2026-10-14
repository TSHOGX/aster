package evolution

import (
	"encoding/json"
	"testing"
)

func TestTemplateWorkflowCompatibility(t *testing.T) {
	w := simpleWorkflow()
	raw, err := w.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	ref := Revision{w.ID, 1, Digest(raw)}
	template := Template{Ref: Revision{"template", 1, Digest([]byte("template"))}, Goal: "Corrections", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: w.Inputs, Outputs: w.Outputs, Profile: "authored-v1", Workflows: []Revision{ref}, SuggestedPrefix: "lessons", Evaluation: EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
	if err := template.SupportsWorkflow(ref, w, "authored-v1"); err != nil {
		t.Fatal(err)
	}
	newer := ref
	newer.Revision++
	if template.SupportsWorkflow(newer, w, "authored-v1") == nil {
		t.Fatal("silent revision upgrade accepted")
	}
	if template.SupportsWorkflow(ref, w, "shared-assets-v1") == nil {
		t.Fatal("profile conversion inferred")
	}
	template.Outputs = map[string]string{"patch": Digest([]byte("other"))}
	if template.SupportsWorkflow(ref, w, "authored-v1") == nil {
		t.Fatal("incompatible port accepted")
	}
}
