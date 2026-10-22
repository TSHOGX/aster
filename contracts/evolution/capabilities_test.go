package evolution

import (
	"context"
	"testing"
)

func TestServiceWorkflowCapabilityMatching(t *testing.T) {
	w := simpleWorkflow()
	service := Service{Ref: Revision{"native", 1, Digest([]byte("service"))}, Kind: "evolution", ConnectionID: "native", RunnerDigest: Digest([]byte("runner")), Enabled: true, Capabilities: Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: []string{"agent", "transform"}, EffectivePrompt: true, NodeTrace: true}}
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

func TestServiceChecksNestedWorkflowCapabilities(t *testing.T) {
	wrapper, body := nestedFixture(t)
	service := Service{Ref: Revision{"native", 1, Digest([]byte("service"))}, Kind: "evolution", ConnectionID: "native", RunnerDigest: Digest([]byte("runner")), Enabled: true, Capabilities: Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: []string{"subgraph"}}}
	if err := service.SupportsWorkflow(wrapper); err != nil {
		t.Fatal(err)
	}
	if service.SupportsClosure(context.Background(), wrapper, fixtureResolver{body.ID: body}) == nil {
		t.Fatal("nested unsupported agent accepted")
	}
	service.Capabilities.NodeKinds = []string{"subgraph", "agent", "transform"}
	if err := service.SupportsClosure(context.Background(), wrapper, fixtureResolver{body.ID: body}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityDeclarations(t *testing.T) {
	for _, c := range []Capabilities{{ProtocolVersion: 1, NodeKinds: []string{"agent"}}, {ProtocolVersion: 1, WorkflowVersions: []int{1, 1}}, {ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: []string{"shell"}}, {ProtocolVersion: 1, EditableGraph: true}, {ProtocolVersion: 1, EffectivePrompt: true}} {
		if c.Validate() == nil {
			t.Fatal("invalid advertisement accepted", c)
		}
	}
	if err := (Capabilities{ProtocolVersion: 1}).Validate(); err != nil {
		t.Fatal("opaque provider rejected", err)
	}
}
