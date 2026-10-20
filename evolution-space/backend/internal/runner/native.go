package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/workflow"
)

type NativeAuthority interface {
	Authority
	AuthorizeArtifact(context.Context, evolution.Run, evolution.ArtifactRef) error
	OutputGrant(context.Context, evolution.Run) (string, error)
}
type Transform = func(context.Context, workflow.Call) (workflow.Values, error)
type ModelProvider interface {
	projections.Provider
	projections.ProviderRecovery
}
type Model struct {
	Provider ModelProvider
	Ceiling  projections.RunUsage
	Tools    map[evolution.Revision]bool
	// Disabled models still reconcile retained calls but admit no new work.
	Disabled bool
}

// ModelSource resolves administrator-managed models at call time, so saved
// connection revisions apply without restarting the worker.
type ModelSource interface {
	Model(context.Context, evolution.Revision) (Model, bool)
}

// NativeRegistry contains only operator-registered pure implementations and
// authenticated model adapters. Workflow JSON cannot choose executable code.
type NativeRegistry struct {
	Store      *projections.Store
	Schemas    map[string]json.RawMessage
	Transforms map[evolution.Revision]Transform
	Models     map[evolution.Revision]Model
	Source     ModelSource
}

// Model returns a static registration first, then a managed connection.
func (r *NativeRegistry) Model(ctx context.Context, ref evolution.Revision) (Model, bool) {
	if model, ok := r.Models[ref]; ok {
		return model, true
	}
	if r.Source != nil {
		return r.Source.Model(ctx, ref)
	}
	return Model{}, false
}

// ModelRevisions lists registered model pins in stable order for browsers.
func (r *NativeRegistry) ModelRevisions() []evolution.Revision {
	result := make([]evolution.Revision, 0, len(r.Models))
	for ref := range r.Models {
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID || result[i].ID == result[j].ID && result[i].Revision < result[j].Revision
	})
	return result
}
func (r *NativeRegistry) ResolveWorkflow(ctx context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	return r.Store.Workflow(ctx, ref)
}
func (r *NativeRegistry) Schema(_ context.Context, digest string) (json.RawMessage, error) {
	raw, ok := r.Schemas[digest]
	if !ok {
		return nil, fmt.Errorf("schema not registered")
	}
	return bytes.Clone(raw), nil
}
func (r *NativeRegistry) Check(ctx context.Context, node evolution.Node) error {
	switch node.Kind {
	case "transform", "validator":
		if node.ImplementationRef == nil || r.Transforms[*node.ImplementationRef] == nil {
			return fmt.Errorf("implementation not registered")
		}
	case "agent":
		if node.ModelRef == nil {
			return fmt.Errorf("model reference required")
		}
		model, ok := r.Model(ctx, *node.ModelRef)
		if !ok || model.Provider == nil || model.Ceiling.Tokens < 1 || model.Ceiling.CostMicros < 0 {
			return fmt.Errorf("budgeted model not registered")
		}
		if model.Disabled {
			return fmt.Errorf("model connection is disabled or has no key")
		}
		for _, tool := range node.Tools {
			if !model.Tools[tool] {
				return fmt.Errorf("tool not allowed by model adapter")
			}
		}
	}
	return nil
}

type NativeExecutor struct {
	Registry  *NativeRegistry
	Authority NativeAuthority
}
type nativeRuntime struct {
	executor *NativeExecutor
	lease    projections.RunLease
	run      evolution.Run
	grant    string
}

func (r nativeRuntime) AuthorizeProviderCall(ctx context.Context, _ projections.RunLease, call projections.ProviderCall) error {
	if err := r.executor.Authority.AuthorizeRun(ctx, r.run, false); err != nil {
		return err
	}
	return r.executor.Authority.AuthorizeArtifact(ctx, r.run, call.Request)
}
func (r nativeRuntime) Execute(ctx context.Context, call workflow.Call) (workflow.Values, error) {
	if err := r.executor.Authority.AuthorizeRun(ctx, r.run, false); err != nil {
		return nil, err
	}
	if err := r.executor.Registry.Check(ctx, call.Node); err != nil {
		return nil, err
	}
	if call.Node.Kind != "agent" {
		return r.executor.Registry.Transforms[*call.Node.ImplementationRef](ctx, call)
	}
	model, _ := r.executor.Registry.Model(ctx, *call.Node.ModelRef)
	raw, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > r.run.Budget.MaxArtifactBytes {
		return nil, projections.ErrRunBudget
	}
	artifact := evolution.ArtifactRef{ID: "call-" + strings.TrimPrefix(evolution.Digest([]byte(r.run.ID+"\x00"+call.ID)), "sha256:"), GroupID: r.run.GroupID, SchemaDigest: evolution.Digest([]byte("native-node-call-v1")), Digest: evolution.Digest(raw), Size: int64(len(raw)), MediaType: "application/json", GrantID: r.grant, PolicyRevision: r.run.Pins.SourcePolicyRevision}
	if err = r.executor.Authority.AuthorizeArtifact(ctx, r.run, artifact); err != nil {
		return nil, err
	}
	if err = r.executor.Registry.Store.SealArtifact(ctx, artifact, bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	outcome, err := r.executor.Registry.Store.ExecuteProviderCall(ctx, r.lease, projections.ProviderCall{ID: call.ID, RunID: r.run.ID, Request: artifact}, model.Ceiling, model.Provider, r)
	if err != nil {
		return nil, err
	}
	if outcome.State != "completed" {
		return nil, workflow.Failure{Code: "provider_failed", Retryable: outcome.State == "failed" && outcome.Retryable && len(call.Node.Tools) == 0}
	}
	if outcome.Output != nil && outcome.Output.Size > r.run.Budget.MaxArtifactBytes {
		return nil, workflow.Failure{Code: "output_artifact_budget"}
	}
	if outcome.Output == nil {
		return nil, workflow.Failure{Code: "missing_provider_output"}
	}
	if err = r.executor.Authority.AuthorizeArtifact(ctx, r.run, *outcome.Output); err != nil {
		return nil, err
	}
	output, err := r.executor.Registry.Store.NonDatasetArtifact(ctx, *outcome.Output)
	if err != nil {
		return nil, err
	}
	values, err := evolution.Decode[workflow.Values](bytes.NewReader(output))
	if err != nil {
		return nil, workflow.Failure{Code: "invalid_model_output"}
	}
	return values, nil
}
func (e *NativeExecutor) Execute(ctx context.Context, lease projections.RunLease, run evolution.Run) (string, error) {
	if e.Registry == nil || e.Registry.Store == nil || e.Authority == nil || lease.RunID != run.ID {
		return "", fmt.Errorf("native execution dependencies required")
	}
	if err := e.Authority.AuthorizeRun(ctx, run, false); err != nil {
		return "", err
	}
	deadline := run.CreatedAt.Add(time.Duration(run.Budget.TimeoutSeconds) * time.Second)
	if !time.Now().Before(deadline) {
		if err := e.Cleanup(ctx, lease, run); err != nil {
			return "", err
		}
		return "failed", nil
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	graph, err := e.Registry.Store.Workflow(ctx, run.Pins.Workflow)
	if err != nil {
		return "", err
	}
	if run.DraftTest != nil {
		if err = e.Registry.Store.VerifyDraftTestScope(ctx, run); err != nil {
			return "", err
		}
	}
	if len(run.Pins.InputPorts) != len(graph.Inputs) {
		return "failed", nil
	}
	inputs := workflow.Values{}
	for name, schema := range graph.Inputs {
		artifact, ok := run.Pins.InputPorts[name]
		if !ok || artifact.SchemaDigest != schema || artifact.GroupID != run.GroupID {
			return "failed", nil
		}
		declared := false
		for _, input := range run.Pins.Inputs {
			if input == artifact {
				declared = true
				break
			}
		}
		if !declared {
			return "failed", nil
		}
		if err = e.Authority.AuthorizeArtifact(ctx, run, artifact); err != nil {
			return "", err
		}
		raw, err := e.Registry.Store.NonDatasetArtifact(ctx, artifact)
		if err != nil {
			return "", err
		}
		inputs[name] = raw
	}
	grant, err := e.Authority.OutputGrant(ctx, run)
	if err != nil {
		return "", err
	}
	journal := projections.WorkflowJournal{Store: e.Registry.Store, Lease: lease, GrantID: grant}
	engine := workflow.Engine{Catalog: e.Registry, Runtime: nativeRuntime{e, lease, run, grant}, Journal: journal}
	if run.Experiment != nil {
		records, err := e.Registry.Store.ExperimentReuse(ctx, run)
		if err != nil {
			return "", err
		}
		selected := map[string]workflow.Record{}
		for _, record := range records {
			selected[record.Call.Instance] = record
		}
		engine.Journal = experimentJournal{WorkflowJournal: journal, authority: e.Authority, run: run, records: selected}
	}
	var result any
	if run.DraftTest != nil && run.DraftTest.Kind != "workflow" {
		result, err = engine.ExecuteDraftScope(ctx, run.Pins.Workflow, inputs)
	} else {
		result, err = engine.Execute(ctx, run.Pins.Workflow, inputs)
	}
	if errors.Is(err, workflow.ErrPauseRequested) {
		return "paused", nil
	}
	if err != nil {
		var failure workflow.NodeFailure
		if errors.As(err, &failure) || errors.Is(err, workflow.ErrAttemptLimit) || errors.Is(err, projections.ErrRunBudget) {
			if cleanupErr := e.Cleanup(ctx, lease, run); cleanupErr != nil {
				return "", cleanupErr
			}
			return "failed", nil
		}
		return "", err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > run.Budget.MaxArtifactBytes {
		return "failed", nil
	}
	schemas, _ := json.Marshal(graph.Outputs)
	if run.DraftTest != nil && run.DraftTest.Kind != "workflow" {
		schemas, _ = json.Marshal(map[string]any{"kind": "draft-scope-result-v1", "ports": graph.Outputs})
	}
	artifact := evolution.ArtifactRef{ID: "result-" + strings.TrimPrefix(evolution.Digest([]byte(run.ID)), "sha256:"), GroupID: run.GroupID, SchemaDigest: evolution.Digest(schemas), Digest: evolution.Digest(raw), Size: int64(len(raw)), MediaType: "application/json", GrantID: grant, PolicyRevision: run.Pins.SourcePolicyRevision}
	if err = e.Authority.AuthorizeArtifact(ctx, run, artifact); err != nil {
		return "", err
	}
	if err = e.Registry.Store.SealArtifact(ctx, artifact, bytes.NewReader(raw)); err != nil {
		return "", err
	}
	previous, err := e.Registry.Store.LatestRunCheckpoint(ctx, run.GroupID, run.ID)
	if err == nil && previous.ID == "workflow-result" {
		if previous.Artifact != artifact {
			return "", projections.ErrConflict
		}
		return "completed", nil
	}
	if err != nil && !errors.Is(err, projections.ErrNotFound) {
		return "", err
	}
	if err = e.Authority.AuthorizeRun(ctx, run, false); err != nil {
		return "", err
	}
	if err = e.Registry.Store.SaveRunCheckpoint(ctx, lease, projections.RunCheckpoint{ID: "workflow-result", RunID: run.ID, Fence: lease.Fence, Artifact: artifact}); err != nil {
		return "", err
	}
	return "completed", nil
}
func (e *NativeExecutor) Cleanup(ctx context.Context, lease projections.RunLease, run evolution.Run) error {
	if e.Registry == nil || e.Registry.Store == nil || e.Authority == nil {
		return fmt.Errorf("cleanup dependencies required")
	}
	if err := e.Authority.AuthorizeRun(ctx, run, true); err != nil {
		return err
	}
	after := ""
	for {
		page, err := e.Registry.Store.PendingRunBudgets(ctx, lease, after, 100)
		if err != nil {
			return err
		}
		for _, reservation := range page.Reservations {
			call, err := e.Registry.Store.ProviderCall(ctx, lease, reservation.ID)
			if errors.Is(err, projections.ErrNotFound) {
				if err = e.Registry.Store.SettleRunBudget(ctx, lease, reservation.ID, projections.RunUsage{}); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			raw, err := e.Registry.Store.Artifact(ctx, call.Request)
			if err != nil {
				return err
			}
			frozen, err := evolution.Decode[workflow.Call](bytes.NewReader(raw))
			if err != nil || frozen.Node.ModelRef == nil {
				return fmt.Errorf("invalid retained native call")
			}
			model, ok := e.Registry.Model(ctx, *frozen.Node.ModelRef)
			if !ok || model.Provider == nil {
				return fmt.Errorf("retained model unavailable")
			}
			if _, err = e.Registry.Store.RecoverProviderCall(ctx, lease, call.ID, model.Provider, nativeRuntime{executor: e, lease: lease, run: run}); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		after = page.NextAfter
	}
}
func (r nativeRuntime) AuthorizeProviderRecovery(ctx context.Context, _ projections.RunLease, _ projections.ProviderCall) error {
	return r.executor.Authority.AuthorizeRun(ctx, r.run, true)
}
