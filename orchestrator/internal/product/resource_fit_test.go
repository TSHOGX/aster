package product

import (
	"math"
	"slices"
	"testing"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func TestEnvironmentResourceFitAccountsForAllOwnedAndSharedSlots(t *testing.T) {
	environment := bootstrapSeedFixture(t).Environment
	artifacts := []BuiltinVolume{}
	for _, role := range []string{"agent_config", "shared_assets", "data"} {
		artifact := BuiltinVolume{PublicationPurpose: map[string]string{"agent_config": "config", "shared_assets": "shared_assets", "data": "builtin"}[role], Reference: ArtifactReference{ID: role, Version: "v1", Digest: content.Digest(nil)}, MinimumBytes: 1, SourceCapacityBytes: 1, Roles: []string{role}}
		artifacts = append(artifacts, artifact)
		destination := map[string]string{"agent_config": "/opt/agent-config", "shared_assets": "/opt/shared-assets", "data": "/datasets/reference"}[role]
		environment.Mounts = append(environment.Mounts, EnvironmentMount{Key: role, Role: role, Destination: destination, Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: artifact.Reference.ID, Version: artifact.Reference.Version, Digest: artifact.Reference.Digest}})
	}
	environment.Mounts = append(environment.Mounts, EnvironmentMount{Key: "copy", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", Capacity: MountCapacity{Bytes: 30}, Source: MountSource{Kind: "builtin", ArtifactID: "data", Version: "v1", Digest: content.Digest(nil)}})
	values := map[ResourceDimension]EffectiveResource{}
	for dimension, value := range map[ResourceDimension]int64{ResourceCPU: 1, ResourceMemory: 1, ResourceOverlay: 1, ResourceAggregateCPU: 1, ResourceAggregateMemory: 1, ResourceWorkspace: 20, ResourceRuntime: 10, ResourceVolume: 30, ResourceProjectVolumes: 60, ResourcePersistent: 60, ResourceMounts: 6} {
		values[dimension] = EffectiveResource{Effective: value}
	}
	plan, issues, err := EnvironmentResourceFit(environment, runtimerelease.DefaultMountContract(), artifacts, values, 8)
	must(t, err)
	if len(issues) != 0 || plan.OwnedBytes != 60 || plan.LargestOwnedBytes != 30 || len(plan.MountPlan) != 6 {
		t.Fatal(plan, issues)
	}
	slices.Reverse(environment.Mounts)
	reordered, issues, err := EnvironmentResourceFit(environment, runtimerelease.DefaultMountContract(), artifacts, values, 8)
	must(t, err)
	if len(issues) != 0 || reordered.OwnedBytes != plan.OwnedBytes {
		t.Fatal(reordered, issues)
	}
	for _, dimension := range []ResourceDimension{ResourceVolume, ResourceProjectVolumes, ResourcePersistent, ResourceMounts} {
		old := values[dimension]
		values[dimension] = EffectiveResource{Effective: old.Effective - 1}
		_, issues, err := EnvironmentResourceFit(environment, runtimerelease.DefaultMountContract(), artifacts, values, 8)
		must(t, err)
		if len(issues) != 1 || issues[0].Code != "quota_exceeded" || issues[0].Dimension != dimension {
			t.Fatal(dimension, issues)
		}
		values[dimension] = old
	}
	// Seed minima are structural errors, not a reason to resize or substitute empty storage.
	artifacts[2].MinimumBytes = 31
	_, issues, err = EnvironmentResourceFit(environment, runtimerelease.DefaultMountContract(), artifacts, values, 8)
	must(t, err)
	if len(issues) != 1 || issues[0].Code != "environment_invalid" || issues[0].Field == "" {
		t.Fatal(issues)
	}
	artifacts[2].MinimumBytes = 1
	environment.Mounts[0].Capacity.Bytes = math.MaxInt64
	_, issues, err = EnvironmentResourceFit(environment, runtimerelease.DefaultMountContract(), artifacts, values, 8)
	must(t, err)
	if len(issues) != 1 || issues[0].Code != "resource_overflow" {
		t.Fatal(issues)
	}
}
