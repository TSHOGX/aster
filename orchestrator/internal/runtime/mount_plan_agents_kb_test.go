package runtime

import (
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/store"
)

func baseAgentsKBMountPlanInputs(dir string) SandboxMountPlanInputs {
	return SandboxMountPlanInputs{
		Generation: store.RuntimeGenerationDetails{
			ControlDirPath: filepath.Join(dir, "run", "control", "gen-1"),
			BridgeDirPath:  filepath.Join(dir, "run", "bridge", "gen-1"),
		},
		WorkspaceHostPath: filepath.Join(dir, "sessions", "sess-1"),
		AgentHomeHostPath: filepath.Join(dir, "agent-homes", "sess-1", "sh"),
	}
}

func TestBuildSandboxMountPlanIncludesAgentsKBWhenSet(t *testing.T) {
	dir := t.TempDir()
	kbPath := filepath.Join(dir, "agents-kb", "snapshots", "seed")
	input := baseAgentsKBMountPlanInputs(dir)
	input.AgentsKBHostPath = kbPath

	plan, err := BuildSandboxMountPlan(input)
	if err != nil {
		t.Fatalf("build mount plan: %v", err)
	}

	var found *MountPlanMount
	for i := range plan.Content {
		if plan.Content[i].Name == "agents_kb" {
			found = &plan.Content[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected agents_kb mount in plan: %+v", plan.Content)
	}
	if found.Destination != "/agents" {
		t.Fatalf("agents_kb destination=%q want /agents", found.Destination)
	}
	if found.Mode != "ro" {
		t.Fatalf("agents_kb mode=%q want ro", found.Mode)
	}
	if found.Source != kbPath {
		t.Fatalf("agents_kb source=%q want %q", found.Source, kbPath)
	}
}

func TestBuildSandboxMountPlanOmitsAgentsKBWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	input := baseAgentsKBMountPlanInputs(dir)
	// AgentsKBHostPath deliberately left empty.

	plan, err := BuildSandboxMountPlan(input)
	if err != nil {
		t.Fatalf("build mount plan: %v", err)
	}

	if mountByDestination(plan.SpecMounts(), "/agents") != nil {
		t.Fatalf("agents_kb mount must be absent when path empty: %+v", plan.Content)
	}
}
