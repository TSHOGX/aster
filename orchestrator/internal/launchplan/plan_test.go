package launchplan

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
	"testing"
)

func manifestFixture() map[string]any {
	files := map[string]string{}
	for _, path := range []string{"/usr/sbin/sandboxd", "/usr/local/bin/harness-khora-launcher", "/usr/local/bin/harness-bridge-client", "/usr/local/bin/harness-shell-agent", "/usr/local/bin/harness-workspace-helper", "/usr/local/bin/harness-journal-replay", "/usr/local/lib/harness_files.py", "/usr/local/lib/harness_launch.py", "/usr/local/lib/harness_stream.py", "/usr/local/lib/harness_recovery.py"} {
		files[path] = "sha256:" + strings.Repeat("a", 64)
	}
	return map[string]any{"schema_version": 1, "runtime": map[string]any{"backend": "khora", "engine_revision": EngineRevision, "engine_module": EngineModule, "bridge_stream_version": 1, "home": "/root", "retained_overlay": true, "launcher": "/usr/local/bin/harness-khora-launcher", "files": files}, "drivers": []map[string]any{{"driver_id": "sh", "bridge_protocol": "harness_bridge_v2", "bridge_protocol_version": 2, "turn_input_schema": "RunTurn", "binary_path": "/usr/local/bin/harness-shell-agent", "installed_binary_digest": "sha256:" + strings.Repeat("a", 64), "capabilities": []string{"process_stream", "filesystem", "durable_workspace", "cold_restart", "signal", "pty"}}}}
}
func basePlan() execution.LaunchPlan {
	return execution.LaunchPlan{TemplateID: "battery", CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20, Mounts: []sandbox.Mount{{VolumeID: "assets", Destination: "/root/.agents", Access: "shared-ro"}}}
}
func TestReleasePlansRemainImmutableAndDigestBound(t *testing.T) {
	raw, _ := json.Marshal(manifestFixture())
	hash, _ := Digest(raw)
	plan := basePlan()
	source, err := Load(strings.NewReader(string(raw)), hash, map[string]execution.LaunchPlan{"sh": plan})
	if err != nil {
		t.Fatal(err)
	}
	plan.Mounts[0].VolumeID = "changed"
	first, err := source.Plan(context.Background(), execution.Session{Driver: "sh"}, "one")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mounts[0].VolumeID != "assets" || first.ManifestDigest != hash || !validDigest(first.SpecDigest) {
		t.Fatalf("release intent mutated: %+v", first)
	}
	first.Mounts[0].VolumeID = "other"
	second, err := source.Plan(context.Background(), execution.Session{Driver: "sh"}, "two")
	if err != nil || second.Mounts[0].VolumeID != "assets" || second.SpecDigest != first.SpecDigest {
		t.Fatalf("caller changed frozen plan: %+v %v", second, err)
	}
	if _, err := source.Plan(context.Background(), execution.Session{Driver: "pi"}, "three"); err == nil {
		t.Fatal("unconfigured driver started")
	}
}
func TestReleasePlansRejectLegacyTemplatesAndUnpinnedConfiguration(t *testing.T) {
	for _, fault := range []string{"manifest", "home", "engine", "component", "capability", "unlimited", "mount", "secret", "spec"} {
		t.Run(fault, func(t *testing.T) {
			metadata := manifestFixture()
			plan := basePlan()
			runtime := metadata["runtime"].(map[string]any)
			switch fault {
			case "home":
				runtime["home"] = "/agent-home"
			case "engine":
				runtime["engine_revision"] = "changed"
			case "component":
				delete(runtime["files"].(map[string]string), "/usr/sbin/sandboxd")
			case "capability":
				metadata["drivers"].([]map[string]any)[0]["capabilities"] = []string{"checkpoint"}
			case "unlimited":
				plan.MemoryBytes = 0
			case "mount":
				plan.Mounts[0].Access = "exclusive"
			case "secret":
				plan.Environment = []string{"API_KEY=secret"}
			case "spec":
				plan.SpecDigest = "sha256:" + strings.Repeat("c", 64)
			}
			raw, _ := json.Marshal(metadata)
			hash, _ := Digest(raw)
			if fault == "manifest" {
				hash = "sha256:" + strings.Repeat("f", 64)
			}
			if _, err := Load(strings.NewReader(string(raw)), hash, map[string]execution.LaunchPlan{"sh": plan}); err == nil {
				t.Fatalf("invalid %s release plan accepted", fault)
			}
		})
	}
}
