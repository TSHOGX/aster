package product

import (
	"strings"
	"testing"
)

func TestEnvironmentCompletePlanAndMountValidation(t *testing.T) {
	seed := bootstrapSeedFixture(t)
	base := seed.Environment
	base.Mounts = append(base.Mounts, EnvironmentMount{Key: "data", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", Source: MountSource{Kind: "empty"}, Capacity: MountCapacity{Bytes: 30}})
	resolved, err := ResolveEnvironmentStructure(base, nil, 20, 10, 8)
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
		{"system path", func(e *DefaultEnvironment) { e.Mounts[2].Destination = "/opt/agent-config" }},
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
			if _, err := ResolveEnvironmentStructure(candidate, nil, 20, 10, 8); err == nil {
				t.Fatal("invalid mount plan accepted")
			}
		})
	}
	if _, err = ResolveEnvironmentStructure(base, nil, 20, 10, 2); err == nil {
		t.Fatal("mount limit ignored")
	}
}
