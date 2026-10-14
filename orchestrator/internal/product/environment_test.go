package product

import (
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
	"strings"
	"testing"
)

func TestEnvironmentCompletePlanAndMountValidation(t *testing.T) {
	seed := bootstrapSeedFixture(t)
	seed.Sandbox.Manifest.Mounts.BlockedPaths = []string{"/opt/image-file"}
	base := seed.Environment
	base.Mounts = append(base.Mounts, EnvironmentMount{Key: "data", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", Source: MountSource{Kind: "empty"}, Capacity: MountCapacity{Bytes: 30}})
	resolved, err := ResolveEnvironmentStructure(base, seed.Sandbox.Manifest.Mounts, nil, 20, 10, 8)
	if err != nil || resolved.OwnedBytes != 60 || resolved.LargestOwnedBytes != 30 || len(resolved.MountPlan) != 3 {
		t.Fatal(resolved, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*DefaultEnvironment)
	}{
		{"overlap", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/workspace/data" }},
		{"same path", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/runtime" }},
		{"duplicate key", func(e *DefaultEnvironment) { e.Mounts[2].Key = "runtime" }},
		{"missing role", func(e *DefaultEnvironment) { e.Mounts = e.Mounts[1:] }},
		{"shared empty", func(e *DefaultEnvironment) { e.Mounts[2].Storage = "shared_readonly" }},
		{"protected", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/etc/secret" }},
		{"image file", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/opt/image-file/child" }},
		{"root", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/" }},
		{"traversal", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/data/../other" }},
		{"control", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/data\n" }},
		{"overlong", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/" + strings.Repeat("x", 4096) }},
		{"capacity conflict", func(e *DefaultEnvironment) { e.Mounts[2].Capacity.Setting = ResourceWorkspace }},
		{"readonly workspace", func(e *DefaultEnvironment) { e.Mounts[0].Access = "read_only" }},
		{"extra source fields", func(e *DefaultEnvironment) { e.Mounts[2].Source.ArtifactID = "untrusted" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Mounts = append([]EnvironmentMount{}, base.Mounts...)
			tc.mutate(&candidate)
			if _, err := ResolveEnvironmentStructure(candidate, seed.Sandbox.Manifest.Mounts, nil, 20, 10, 8); err == nil {
				t.Fatal("invalid mount plan accepted")
			}
		})
	}
	if _, err = ResolveEnvironmentStructure(base, seed.Sandbox.Manifest.Mounts, nil, 20, 10, 2); err == nil {
		t.Fatal("mount limit ignored")
	}
}

func TestEnvironmentUsesSelectedManifestPathsAndRequiredPublications(t *testing.T) {
	seed := bootstrapSeedFixture(t)
	contract := runtimerelease.DefaultMountContract()
	environment := seed.Environment
	environment.Mounts[0].Destination = "/workspaces/project"
	environment.Mounts[1].Destination = "/state/project"
	artifacts := []BuiltinVolume{}
	for _, required := range contract.RequiredPublications {
		reference := ArtifactReference{ID: required.ArtifactID, Version: "v1", Digest: content.Digest(nil)}
		artifacts = append(artifacts, BuiltinVolume{PublicationPurpose: map[string]string{"agent_config": "config", "shared_assets": "shared_assets"}[required.Role], Reference: reference, MinimumBytes: 1, SourceCapacityBytes: 1, Roles: []string{required.Role}})
		environment.Mounts = append(environment.Mounts, EnvironmentMount{Key: required.Role, Role: required.Role, Destination: required.Destination, Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: reference.ID, Version: reference.Version, Digest: reference.Digest}})
	}
	plan, err := ResolveEnvironmentStructure(environment, contract, artifacts, 20, 10, 8)
	must(t, err)
	if plan.OwnedBytes != 30 || len(plan.MountPlan) != 4 {
		t.Fatal(plan)
	}
	missing := environment
	missing.Mounts = missing.Mounts[:3]
	_, err = ResolveEnvironmentStructure(missing, contract, artifacts, 20, 10, 8)
	wantError(t, err, ErrInvalid)
	environment.Mounts[3].Destination = "/other"
	_, err = ResolveEnvironmentStructure(environment, contract, artifacts, 20, 10, 8)
	wantError(t, err, ErrInvalid)
	environment.Mounts[3].Destination = contract.RequiredPublications[1].Destination
	contract.BlockedPaths = []string{"/workspaces"}
	_, err = ResolveEnvironmentStructure(environment, contract, artifacts, 20, 10, 8)
	wantError(t, err, ErrInvalid)
}
