package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationResolvesExactCompatibleCatalog(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = workflow.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	workflowRef := evolution.Revision{ID: workflow.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = s.PublishWorkflow(ctx, workflowRef, raw); err != nil {
		t.Fatal(err)
	}
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	template := evolution.Template{Ref: evolution.Revision{ID: "template", Revision: 1}, Goal: "Extract skills", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: workflow.Inputs, Outputs: workflow.Outputs, Profile: "authored-v1", Workflows: []evolution.Revision{workflowRef}, SuggestedPrefix: "skills", Evaluation: off}
	template.Ref.Digest, _ = evolution.DefinitionDigest(template)
	raw, _ = json.Marshal(template)
	if err = s.PublishDefinition(ctx, "template", raw); err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	seen := map[string]bool{}
	for _, node := range workflow.Nodes {
		if !seen[node.Kind] {
			kinds = append(kinds, node.Kind)
			seen[node.Kind] = true
		}
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: evolution.Digest([]byte("runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: kinds}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	raw, _ = json.Marshal(service)
	if err = s.PublishDefinition(ctx, "service", raw); err != nil {
		t.Fatal(err)
	}
	budget := evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: budget, Sources: []evolution.SourceBinding{{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{"group"}, DerivativePublication: true}}, Bindings: []evolution.Binding{{ID: "binding", Template: template.Ref, Workflow: workflowRef, Service: service.Ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: off, Budget: budget, Trigger: "manual"}}}
	if err = s.ValidateConfiguration(ctx, config); err != nil {
		t.Fatal("compatible disabled evaluation", err)
	}
	configRaw, _ := json.Marshal(config)
	if err = s.Adopt(ctx, Projection{OperationID: "valid", Digest: evolution.Digest(configRaw), Configuration: config}); err != nil {
		t.Fatal("compatible projection rejected", err)
	}
	config.Revision = 2
	config.Bindings[0].Workflow.Revision = 2
	if err = s.ValidateConfiguration(ctx, config); err == nil {
		t.Fatal("missing workflow accepted")
	}
	configRaw, _ = json.Marshal(config)
	if err = s.Adopt(ctx, Projection{OperationID: "missing", Digest: evolution.Digest(configRaw), Configuration: config}); !errors.Is(err, ErrCatalog) {
		t.Fatal("missing workflow projection accepted", err)
	}
	if _, err = s.Read(ctx, config.GroupID, config.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid projection persisted", err)
	}
	config.Bindings[0].Workflow = workflowRef
	config.Bindings[0].Output.Profile = "shared-assets-v1"
	if err = s.ValidateConfiguration(ctx, config); err == nil {
		t.Fatal("wrong output profile accepted")
	}
	config.Bindings[0].Output.Profile = "authored-v1"
	service.Ref.Revision = 2
	service.Enabled = false
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	raw, _ = json.Marshal(service)
	if err = s.PublishDefinition(ctx, "service", raw); err != nil {
		t.Fatal(err)
	}
	config.Bindings[0].Service = service.Ref
	if err = s.ValidateConfiguration(ctx, config); err == nil {
		t.Fatal("disabled service accepted")
	}
}
