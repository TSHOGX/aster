package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func configurationFixture(t *testing.T) (Config, string) {
	t.Helper()
	directory := t.TempDir()
	digest := "sha256:" + strings.Repeat("0", 64)
	manifest := runtimerelease.Manifest{Version: 2, ReaderProtocol: 2, Platform: "linux/amd64", EngineModule: runtimerelease.EngineModule, EngineRevision: runtimerelease.EngineRevision, AgentProtocol: 1, Workspace: "/workspace", RuntimeState: "/runtime", PiPackage: runtimerelease.PiPackage, PiVersion: runtimerelease.PiVersion, PiIntegrity: runtimerelease.PiIntegrity, NodeVersion: runtimerelease.NodeVersion, NativeTree: digest, Files: map[string]string{}}
	for _, component := range runtimerelease.Components {
		manifest.Files[component] = digest
	}
	raw, _ := runtimerelease.Bytes(manifest)
	digest, _ = runtimerelease.Digest(raw)
	if err := os.WriteFile(filepath.Join(directory, "runtime.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := defaults()
	cfg.Version = 1
	cfg.StateDirectory = "state"
	cfg.Template = Template{ID: "test", Manifest: "runtime.json", Digest: digest}
	cfg.Engine.Endpoint = "http://127.0.0.1:18191"
	cfg.Engine.StoreID = "store"
	cfg.Auth.DevelopmentActor = "local"
	cfg.Project = product.Profile{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 1 << 30, RuntimeBytes: 1 << 29}
	return cfg, filepath.Join(directory, "application.json")
}
func TestConfigurationHasOneStrictProjectSurface(t *testing.T) {
	cfg, path := configurationFixture(t)
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(loaded.StateDirectory) || loaded.Profile().StoreID != "store" || loaded.Profile().ReleaseDigest != cfg.Template.Digest {
		t.Fatal(loaded)
	}
	var document map[string]any
	json.Unmarshal(raw, &document)
	document["plans"] = map[string]any{"sh": map[string]string{"template": "old"}}
	raw, _ = json.Marshal(document)
	os.WriteFile(path, raw, 0600)
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("retired driver-plan configuration accepted")
	}
}
func TestConfigurationRefusesImplicitAuthAndUserSelectedBindings(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.Auth.DevelopmentActor = "" }, func(c *Config) { c.Listen = "0.0.0.0:8080" }, func(c *Config) { c.Project.StoreID = "another" }, func(c *Config) { c.Project.TemplateID = "another" }, func(c *Config) { c.Limits.AgentsPerProject = 1000 }} {
		cfg, path := configurationFixture(t)
		change(&cfg)
		raw, _ := json.Marshal(cfg)
		os.WriteFile(path, raw, 0600)
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("invalid configuration accepted", cfg)
		}
	}
}

func TestGatewayFloorOriginsAreRejectedBeforeStartup(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:8090", "https://[::1]", "http://localhost", "http://169.254.169.254", "http://[fe80::1]"} {
		if !gatewayFloorOrigin(origin) {
			t.Errorf("floor origin accepted: %s", origin)
		}
	}
	if gatewayFloorOrigin("https://192.0.2.1:8090") {
		t.Fatal("host engine TLS endpoint rejected")
	}
}

func TestConfigurationRequiresExplicitVersionAndBoundedFile(t *testing.T) {
	cfg, path := configurationFixture(t)
	raw, _ := json.Marshal(cfg)
	var document map[string]any
	json.Unmarshal(raw, &document)
	delete(document, "version")
	raw, _ = json.Marshal(document)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("missing configuration version accepted")
	}
	raw, _ = json.Marshal(cfg)
	raw = append(raw, []byte(strings.Repeat(" ", 1<<20))...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("oversized configuration accepted")
	}
}
