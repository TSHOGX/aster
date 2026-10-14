package evolution

import "testing"

func TestServiceWorkflowCapabilityMatching(t *testing.T) {
	w := simpleWorkflow()
	service := Service{Ref: Revision{"native", 1, Digest([]byte("service"))}, Kind: "evolution", ConnectionID: "native", RunnerDigest: Digest([]byte("runner")), Enabled: true, Capabilities: Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: []string{"agent", "transform"}, EffectivePrompt: true}}
	if err := service.SupportsWorkflow(w); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"opaque", "evaluation"} {
		copy := service
		if kind == "opaque" {
			copy.Capabilities.NodeKinds = nil
		} else {
			copy.Kind = kind
		}
		if copy.SupportsWorkflow(w) == nil {
			t.Fatal("incapable service accepted", kind)
		}
	}
	w.Capabilities = []string{"effective_prompt"}
	if err := service.SupportsWorkflow(w); err != nil {
		t.Fatal(err)
	}
	w.Capabilities = []string{"versioned_models"}
	if service.SupportsWorkflow(w) == nil {
		t.Fatal("moving model capability accepted")
	}
	w.Capabilities = []string{"unknown-extension"}
	if service.SupportsWorkflow(w) == nil {
		t.Fatal("unsupported extension silently ignored")
	}
	service.Enabled = false
	w.Capabilities = nil
	if service.SupportsWorkflow(w) == nil {
		t.Fatal("disabled service accepted")
	}
}
