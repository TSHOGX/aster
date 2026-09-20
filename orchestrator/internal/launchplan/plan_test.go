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
	return execution.LaunchPlan{TemplateID: "battery", CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20, Content: []execution.ContentRequirement{{Purpose: "assets", Destination: "/root/.agents", RevisionDigest: assetsRevision}}}
}

const assetsRevision = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"

// publishedContent is the engine store's publication record as the plan source
// sees it: a revision resolves to a volume only once this store published it.
type publishedContent map[string]string

func (p publishedContent) FindPublishedContent(_ context.Context, store, purpose, revision string) (string, bool, error) {
	volume, ok := p[store+"|"+purpose+"|"+revision]
	return volume, ok, nil
}

func boundSource(t *testing.T, source *Source, content publishedContent) *Source {
	t.Helper()
	bound, err := source.WithPublishedContent("store", content)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}
func TestReleasePlansRemainImmutableAndDigestBound(t *testing.T) {
	raw, _ := json.Marshal(manifestFixture())
	hash, _ := Digest(raw)
	plan := basePlan()
	loaded, err := Load(strings.NewReader(string(raw)), hash, map[string]execution.LaunchPlan{"sh": plan})
	if err != nil {
		t.Fatal(err)
	}
	published := publishedContent{"store|assets|" + assetsRevision: "vol_assets"}
	source := boundSource(t, loaded, published)
	plan.Content[0].RevisionDigest = "sha256:" + strings.Repeat("9", 64)
	first, err := source.Plan(context.Background(), execution.Session{Driver: "sh"}, "one")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mounts[0].VolumeID != "vol_assets" || first.Mounts[0].Access != "shared-ro" || first.ManifestDigest != hash || !validDigest(first.SpecDigest) {
		t.Fatalf("release intent mutated: %+v", first)
	}
	first.Mounts[0].VolumeID = "other"
	first.Content[0].RevisionDigest = "tampered"
	second, err := source.Plan(context.Background(), execution.Session{Driver: "sh"}, "two")
	if err != nil || second.Mounts[0].VolumeID != "vol_assets" || second.SpecDigest != first.SpecDigest {
		t.Fatalf("caller changed frozen plan: %+v %v", second, err)
	}
	if _, err := source.Plan(context.Background(), execution.Session{Driver: "pi"}, "three"); err == nil {
		t.Fatal("unconfigured driver started")
	}
	// An unpublished revision must refuse the launch rather than mount nothing.
	unpublished := boundSource(t, loaded, publishedContent{})
	if _, err := unpublished.Plan(context.Background(), execution.Session{Driver: "sh"}, "four"); err == nil {
		t.Fatal("plan mounted content this engine store never published")
	}
	// An unbound source cannot resolve content at all.
	if _, err := loaded.Plan(context.Background(), execution.Session{Driver: "sh"}, "five"); err == nil {
		t.Fatal("unbound plan source resolved content")
	}
}

// The spec digest names authored content, not engine-local volume identities, so
// the same reviewed revision produces one declaration in any engine store.
func TestSpecDigestCoversRevisionsNotVolumeIdentities(t *testing.T) {
	raw, _ := json.Marshal(manifestFixture())
	hash, _ := Digest(raw)
	loaded, err := Load(strings.NewReader(string(raw)), hash, map[string]execution.LaunchPlan{"sh": basePlan()})
	if err != nil {
		t.Fatal(err)
	}
	one, err := boundSource(t, loaded, publishedContent{"store|assets|" + assetsRevision: "vol_a"}).Plan(context.Background(), execution.Session{Driver: "sh"}, "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := boundSource(t, loaded, publishedContent{"store|assets|" + assetsRevision: "vol_b"}).Plan(context.Background(), execution.Session{Driver: "sh"}, "two")
	if err != nil {
		t.Fatal(err)
	}
	if one.SpecDigest != two.SpecDigest {
		t.Fatalf("volume identity leaked into the spec digest: %s %s", one.SpecDigest, two.SpecDigest)
	}
	if one.Mounts[0].VolumeID == two.Mounts[0].VolumeID {
		t.Fatal("test did not exercise two distinct published volumes")
	}
	changed := basePlan()
	changed.Content[0].RevisionDigest = "sha256:" + strings.Repeat("2", 64)
	other, err := Load(strings.NewReader(string(raw)), hash, map[string]execution.LaunchPlan{"sh": changed})
	if err != nil {
		t.Fatal(err)
	}
	third, err := boundSource(t, other, publishedContent{"store|assets|" + changed.Content[0].RevisionDigest: "vol_a"}).Plan(context.Background(), execution.Session{Driver: "sh"}, "three")
	if err != nil {
		t.Fatal(err)
	}
	if third.SpecDigest == one.SpecDigest {
		t.Fatal("a different authored revision reused a spec digest")
	}
}
func TestReleasePlansRejectLegacyTemplatesAndUnpinnedConfiguration(t *testing.T) {
	for _, fault := range []string{"manifest", "home", "engine", "component", "capability", "unlimited", "mount", "destination", "revision", "secret", "spec"} {
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
				plan.Mounts = []sandbox.Mount{{VolumeID: "assets", Destination: "/root/.agents", Access: "shared-ro"}}
			case "destination":
				plan.Content[0].Destination = "/elsewhere"
			case "revision":
				plan.Content[0].RevisionDigest = "not-a-digest"
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
