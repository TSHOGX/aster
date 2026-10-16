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
	"aster/evaluation-space/internal/examples"
	"aster/evaluation-space/internal/handoff"
	"aster/evaluation-space/internal/identity"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/runner"
	"aster/evaluation-space/internal/studioclient"
	"aster/evaluation-space/internal/workflow"
)

type nativeModelConfig struct {
	Reference evolution.Revision   `json:"reference"`
	Endpoint  string               `json:"endpoint"`
	ClientID  string               `json:"client_id"`
	SecretEnv string               `json:"secret_env"`
	Ceiling   projections.RunUsage `json:"ceiling"`
}
type nativeConfig struct {
	Models          []nativeModelConfig        `json:"models"`
	Schemas         map[string]json.RawMessage `json:"schemas"`
	ExampleBuilders bool                       `json:"example_builders"`
}

func readNativeRegistry(path string, store *projections.Store) (*runner.NativeRegistry, func(), error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	config, err := evolution.Decode[nativeConfig](file)
	if err != nil {
		return nil, nil, err
	}
	if len(config.Models) < 1 || len(config.Models) > 32 || len(config.Schemas) > 1000 {
		return nil, nil, fmt.Errorf("invalid native registry limits")
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
			return nil, nil, fmt.Errorf("invalid model budget/credential reference")
		}
		if _, exists := registry.Models[model.Reference]; exists {
			close()
			return nil, nil, fmt.Errorf("duplicate native model")
		}
		client, err := runner.NewHTTPModel(store, runner.ModelConnection{Endpoint: model.Endpoint, ClientID: model.ClientID, Secret: os.Getenv(model.SecretEnv), Model: model.Reference})
		if err != nil {
			close()
			return nil, nil, err
		}
		clients = append(clients, client)
		registry.Models[model.Reference] = runner.Model{Provider: client, Ceiling: model.Ceiling, Tools: map[evolution.Revision]bool{}}
		if config.ExampleBuilders {
			packages, err := examples.Packages(model.Reference)
			if err != nil {
				close()
				return nil, nil, err
			}
			for _, pkg := range packages {
				for digest, raw := range pkg.Schemas {
					registry.Schemas[digest] = raw
				}
			}
		}
	}
	for digest, raw := range config.Schemas {
		if !manifest.ValidDigest(digest) || evolution.Digest(raw) != digest {
			close()
			return nil, nil, fmt.Errorf("native schema digest mismatch")
		}
		registry.Schemas[digest] = raw
	}
	if config.ExampleBuilders {
		registry.Transforms = examples.Implementations()
	}
	return registry, close, nil
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
