package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/examples"
	"aster/evolution-space/internal/handoff"
	"aster/evolution-space/internal/identity"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/runner"
	"aster/evolution-space/internal/studioclient"
	"aster/evolution-space/internal/workflow"
)

type nativeModelConfig struct {
	Reference evolution.Revision   `json:"reference"`
	Endpoint  string               `json:"endpoint"`
	ClientID  string               `json:"client_id"`
	SecretEnv string               `json:"secret_env"`
	Ceiling   projections.RunUsage `json:"ceiling"`
	Tools     []evolution.Revision `json:"tools"`
}
type nativeConfig struct {
	Models          []nativeModelConfig        `json:"models"`
	Schemas         map[string]json.RawMessage `json:"schemas"`
	ExampleBuilders bool                       `json:"example_builders"`
	// ModelConnections enables administrator-managed connections; with it the
	// static model list may be empty.
	ModelConnections bool `json:"model_connections,omitempty"`
}

// examplePreviewModel only computes model-independent example schemas.
var examplePreviewModel = evolution.Revision{ID: "schema-preview", Revision: 1, Digest: evolution.Digest([]byte("schema-preview"))}

func readNativeRegistry(path string, store *projections.Store) (*runner.NativeRegistry, func(), error) {
	registry, close, _, err := readNativeRegistryConfig(path, store)
	return registry, close, err
}
func readNativeRegistryConfig(path string, store *projections.Store) (*runner.NativeRegistry, func(), nativeConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nativeConfig{}, err
	}
	defer file.Close()
	config, err := evolution.Decode[nativeConfig](file)
	if err != nil {
		return nil, nil, config, err
	}
	if len(config.Models) < 1 && !config.ModelConnections || len(config.Models) > 32 || len(config.Schemas) > 1000 {
		return nil, nil, config, fmt.Errorf("invalid native registry limits")
	}
	registry := &runner.NativeRegistry{Store: store, Schemas: map[string]json.RawMessage{}, Transforms: map[evolution.Revision]runner.Transform{}, Models: map[evolution.Revision]runner.Model{}}
	clients := []*runner.HTTPModel{}
	close := func() {
		for _, client := range clients {
			client.Close()
		}
	}
	environment := regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	for _, model := range config.Models {
		if !environment.MatchString(model.SecretEnv) || model.Ceiling.Tokens < 1 || model.Ceiling.CostMicros < 0 {
			close()
			return nil, nil, config, fmt.Errorf("invalid model budget/credential reference")
		}
		if _, exists := registry.Models[model.Reference]; exists {
			close()
			return nil, nil, config, fmt.Errorf("duplicate native model")
		}
		if len(model.Tools) > 128 {
			close()
			return nil, nil, config, fmt.Errorf("native model tool limit exceeded")
		}
		allowedTools := map[evolution.Revision]bool{}
		for _, tool := range model.Tools {
			if tool.Validate() != nil || allowedTools[tool] {
				close()
				return nil, nil, config, fmt.Errorf("invalid or duplicate native tool revision")
			}
			allowedTools[tool] = true
		}
		client, err := runner.NewHTTPModel(store, runner.ModelConnection{Endpoint: model.Endpoint, ClientID: model.ClientID, Secret: os.Getenv(model.SecretEnv), Model: model.Reference, Tools: model.Tools})
		if err != nil {
			close()
			return nil, nil, config, err
		}
		clients = append(clients, client)
		registry.Models[model.Reference] = runner.Model{Provider: client, Ceiling: model.Ceiling, Tools: allowedTools}
	}
	for digest, raw := range config.Schemas {
		if !manifest.ValidDigest(digest) || evolution.Digest(raw) != digest {
			close()
			return nil, nil, config, fmt.Errorf("native schema digest mismatch")
		}
		registry.Schemas[digest] = raw
	}
	if config.ExampleBuilders {
		// Example schemas do not depend on the model; register them once so a
		// registry with only managed connections still validates example ports.
		packages, err := examples.Packages(examplePreviewModel)
		if err != nil {
			close()
			return nil, nil, config, err
		}
		for _, pkg := range packages {
			for digest, raw := range pkg.Schemas {
				registry.Schemas[digest] = raw
			}
		}
		registry.Transforms = examples.Implementations()
	}
	return registry, close, config, nil
}

type candidatePlanner struct{}

func (candidatePlanner) Plan(run evolution.Run, values workflow.Values, base []manifest.Entry, grant string) (evolution.Patch, []handoff.Artifact, error) {
	candidate, err := examples.DecodeCandidate(values["candidate"])
	if err != nil {
		return evolution.Patch{}, nil, err
	}
	result, err := examples.CandidatePatch(run, run.Fence, candidate, base, grant)
	if err != nil {
		return evolution.Patch{}, nil, err
	}
	artifacts := []handoff.Artifact{}
	for _, artifact := range result.Artifacts {
		artifacts = append(artifacts, handoff.Artifact{Reference: artifact.Reference, Bytes: artifact.Bytes})
	}
	return result.Patch, artifacts, nil
}

type handoffAuthority struct{ runner.CentralAuthority }

func (a handoffAuthority) Check(ctx context.Context, run evolution.Run) error {
	return a.AuthorizeRun(ctx, run, false)
}
func (a handoffAuthority) Artifact(ctx context.Context, run evolution.Run, ref evolution.ArtifactRef) error {
	return a.AuthorizeArtifact(ctx, run, ref)
}
func runNative(ctx context.Context, store *projections.Store, central *identity.ProjectionAuthority, registry *runner.NativeRegistry, studio *studioclient.Client) {
	authority := runner.CentralAuthority{Store: store, Central: central}
	executor := &runner.NativeExecutor{Registry: registry, Authority: authority}
	// Independent bindings execute concurrently; database leases fence duplicate
	// discovery. Handoff/settlement run separately so a slow model cannot block them.
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			worker := runner.Worker{Store: store, ID: fmt.Sprintf("native-worker-%d", index), Authority: authority, Executor: executor}
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = worker.Step(ctx)
				}
			}
		}(index)
	}
	defer workers.Wait()
	transfer := handoff.Worker{Store: store, Studio: studioclient.Results{Client: studio, Store: store, Central: central}, Authority: handoffAuthority{authority}, Planner: candidatePlanner{}}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	settleCursor, handoffCursor, pauseCursor := "", "", ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		pauses, err := store.PendingGenerationPauses(ctx, pauseCursor, 10)
		if err == nil {
			if len(pauses) == 0 {
				pauseCursor = ""
			}
			for _, item := range pauses {
				run, err := store.Run(ctx, item.GroupID, item.RunID)
				if err == nil {
					_ = authority.ReconcilePause(ctx, run)
				}
				pauseCursor = item.RunID
			}
		}
		pending, err := store.UnsettledGenerations(ctx, settleCursor, 10)
		if err == nil {
			if len(pending) == 0 {
				settleCursor = ""
			}
			for _, item := range pending {
				run, err := store.Run(ctx, item.GroupID, item.RunID)
				if err == nil {
					_ = authority.ReconcileTerminal(ctx, run)
				}
				settleCursor = item.RunID
			}
		}
		pending, err = store.PendingHandoffs(ctx, handoffCursor, 10)
		if err == nil {
			if len(pending) == 0 {
				handoffCursor = ""
			}
			for _, item := range pending {
				_ = transfer.Step(ctx, item.GroupID, item.RunID)
				handoffCursor = item.RunID
			}
		}
	}
}
