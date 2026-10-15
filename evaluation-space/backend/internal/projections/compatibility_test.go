package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: budget, Sources: []evolution.SourceBinding{{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{"group"}, DerivativePublication: true}}, Bindings: []evolution.Binding{{ID: "binding", Enabled: true, Template: template.Ref, Workflow: workflowRef, Service: service.Ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: off, Budget: budget, Trigger: "manual"}}}
	if err = s.ValidateConfiguration(ctx, config); err != nil {
		t.Fatal("compatible disabled evaluation", err)
	}
	disable := ServiceStateInput{RequestID: "disable", ServiceID: service.Ref.ID, ExpectedRevision: 1, Enabled: false}
	revision, err := s.SetServiceState(ctx, disable)
	if err != nil || revision != 2 {
		t.Fatal("disable", revision, err)
	}
	if _, err = s.SetServiceState(ctx, disable); err != nil {
		t.Fatal("disable replay", err)
	}
	if err = s.ValidateConfiguration(ctx, config); err == nil {
		t.Fatal("revoked service still valid")
	}
	stale := disable
	stale.RequestID = "stale"
	stale.Enabled = true
	if _, err = s.SetServiceState(ctx, stale); err != ErrConflict {
		t.Fatal("stale state overwrite", err)
	}
	if _, err = s.SetServiceState(ctx, ServiceStateInput{RequestID: "enable", ServiceID: service.Ref.ID, ExpectedRevision: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateConfiguration(ctx, config); err != nil {
		t.Fatal("restored service", err)
	}
	configRaw, _ := json.Marshal(config)
	if err = s.Adopt(ctx, Projection{OperationID: "valid", Digest: evolution.Digest(configRaw), Configuration: config}); err != nil {
		t.Fatal("compatible projection rejected", err)
	}
	pins := evolution.RunPins{ConfigurationRevision: 1, ConfigurationDigest: evolution.Digest(configRaw), SourcePolicyRevision: 1, Template: template.Ref, Workflow: workflowRef, Service: service.Ref, Inputs: []evolution.ArtifactRef{{ID: "snapshot", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("snapshot")), MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}}, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, Output: config.Bindings[0].Output, Evaluation: off}
	pins.Inputs[0].Size = int64(len("snapshot"))
	if err = s.SealArtifact(ctx, pins.Inputs[0], strings.NewReader("snapshot")); err != nil {
		t.Fatal(err)
	}
	admission := RunAdmission{ID: "run", GroupID: "group", BindingID: "binding", OccurrenceID: "manual-one", Pins: pins}
	run, err := s.AdmitRun(ctx, admission)
	if err != nil {
		t.Fatal("run admission", err)
	}
	replay, err := s.AdmitRun(ctx, admission)
	if err != nil || !replay.CreatedAt.Equal(run.CreatedAt) || replay.ResultBranch != run.ResultBranch {
		t.Fatal("run replay changed identity", err)
	}
	duplicate := admission
	duplicate.ID = "duplicate"
	if _, err = s.AdmitRun(ctx, duplicate); err != ErrConflict {
		t.Fatal("duplicate trigger occurrence admitted", err)
	}
	duplicate.OccurrenceID = "manual-two"
	if _, err = s.AdmitRun(ctx, duplicate); err != ErrBindingBusy {
		t.Fatal("competing active run admitted", err)
	}
	changed := admission
	changed.Pins.Output.ReservationGeneration++
	if _, err = s.AdmitRun(ctx, changed); err != ErrConflict {
		t.Fatal("run pins changed on replay", err)
	}
	readRun, err := s.Run(ctx, "group", run.ID)
	if err != nil || readRun.State != "queued" || readRun.Fence != 0 {
		t.Fatal("queued run read", readRun, err)
	}
	if _, err = s.Run(ctx, "other", run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-group run read", err)
	}
	lease, err := s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	readRun, err = s.Run(ctx, "group", run.ID)
	if err != nil || readRun.State != "running" || readRun.Fence != lease.Fence {
		t.Fatal("live run read", readRun, err)
	}
	if err = s.SettleRun(ctx, lease, "completed"); err != nil {
		t.Fatal(err)
	}
	readRun, err = s.Run(ctx, "group", run.ID)
	if err != nil || readRun.State != "completed" || readRun.Pins.ConfigurationDigest != pins.ConfigurationDigest {
		t.Fatal("terminal run read", readRun, err)
	}
	// Settling releases the binding slot without losing occurrence deduplication.
	if _, err = s.AdmitRun(ctx, duplicate); err != nil {
		t.Fatal("settled binding slot retained", err)
	}
	strict := template
	strict.Ref.Revision = 2
	strict.ParametersSchema = json.RawMessage(`{"type":"object","required":["count"],"properties":{"count":{"type":"integer"}}}`)
	strict.Ref.Digest, _ = evolution.DefinitionDigest(strict)
	strictRaw, _ := json.Marshal(strict)
	if err = s.PublishDefinition(ctx, "template", strictRaw); err != nil {
		t.Fatal(err)
	}
	config.Bindings[0].Template = strict.Ref
	config.Revision = 2
	invalidRaw, _ := json.Marshal(config)
	if err = s.Adopt(ctx, Projection{OperationID: "invalid-parameters", Digest: evolution.Digest(invalidRaw), Configuration: config}); !errors.Is(err, ErrCatalog) {
		t.Fatal("invalid parameters adopted", err)
	}
	config.Bindings[0].Template = template.Ref
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
