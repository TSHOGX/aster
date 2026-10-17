package application

import (
	"encoding/json"
	"harness-platform/orchestrator/internal/agentwire"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/runtimerelease"
)

func configurationFixture(t *testing.T) (Config, string) {
	t.Helper()
	directory := t.TempDir()
	digest := "sha256:" + strings.Repeat("0", 64)
	mounts := runtimerelease.DefaultMountContract()
	mounts.RequiredPublications = []runtimerelease.RequiredPublication{}
	manifest := runtimerelease.Manifest{Family: "minimal", ReleaseVersion: "v1", Name: runtimerelease.FamilyName("minimal"), Purpose: runtimerelease.FamilyPurpose("minimal"), Version: runtimerelease.ManifestVersion, HistoryProtocol: runtimerelease.HistoryProtocol, Mounts: mounts, ScheduleProtocol: runtimerelease.ScheduleProtocol, NativeProtocol: runtimerelease.NativeProtocol, ReaderProtocol: runtimerelease.ReaderProtocol, Platform: "linux/amd64", EngineModule: runtimerelease.EngineModule, EngineRevision: runtimerelease.EngineRevision, AgentProtocol: agentwire.Version, Workspace: "/workspace", RuntimeState: "/runtime", PiPackage: runtimerelease.PiPackage, PiVersion: runtimerelease.PiVersion, PiIntegrity: runtimerelease.PiIntegrity, NodeVersion: runtimerelease.NodeVersion, NativeTree: digest, Files: map[string]string{}}
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
	cfg.Limits.MaxMountsPerProject = 8
	cfg.StateDirectory = "state"
	cfg.Template = Template{Version: "v1", ID: "test", Manifest: "runtime.json", Digest: digest}
	cfg.Engine.Endpoint = "http://127.0.0.1:18191"
	cfg.Engine.StoreID = "store"
	cfg.Project = ProjectDefaults{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 1 << 30, RuntimeBytes: 1 << 29}
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
	if !filepath.IsAbs(loaded.StateDirectory) || loaded.Engine.StoreID != "store" || loaded.Template.Digest != cfg.Template.Digest {
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
func TestConfigurationRefusesUserSelectedBindings(t *testing.T) {
	for _, field := range []string{"store_id", "template_id", "volumes", "release_digest", "network_policy_revision"} {
		cfg, path := configurationFixture(t)
		raw, _ := json.Marshal(cfg)
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		document["project"].(map[string]any)[field] = "forged"
		raw, _ = json.Marshal(document)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("user-selected runtime binding accepted", field)
		}
	}
	cfg, path := configurationFixture(t)
	cfg.Limits.AgentsPerProject = 1000
	raw, _ := json.Marshal(cfg)
	os.WriteFile(path, raw, 0600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("invalid capacity accepted")
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

func TestNotificationConfigurationDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name          string
		notifications any
		want          NotificationConfig
	}{
		{"omitted", nil, NotificationConfig{ConversationShared: true, OperatorDirected: true, OperatorBroadcast: true}},
		{"empty", map[string]bool{}, NotificationConfig{ConversationShared: true, OperatorDirected: true, OperatorBroadcast: true}},
		{"partial", map[string]bool{"agent_run_outcome": true, "operator_directed": false}, NotificationConfig{AgentRunOutcome: true, ConversationShared: true, OperatorBroadcast: true}},
		{"all disabled", map[string]bool{"agent_run_outcome": false, "conversation_shared": false, "operator_directed": false, "operator_broadcast": false}, NotificationConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, path := configurationFixture(t)
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err = json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			delete(document, "notifications")
			if tc.notifications != nil {
				document["notifications"] = tc.notifications
			}
			raw, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Notifications != tc.want {
				t.Fatal(loaded.Notifications, tc.want)
			}
		})
	}
}

func TestNotificationConfigurationRejectsUnknownFieldsAndNonBooleans(t *testing.T) {
	for _, setting := range []any{
		map[string]bool{"unknown_source": true},
		map[string]string{"agent_run_outcome": "false"},
		map[string]int{"operator_broadcast": 1},
		[]bool{true},
	} {
		cfg, path := configurationFixture(t)
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err = json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		document["notifications"] = setting
		raw, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadConfig(path); err == nil {
			t.Fatal("invalid notification configuration accepted", setting)
		}
	}
}

func TestConfigurationRejectsRetiredAuthentication(t *testing.T) {
	for _, old := range []string{`{"users_file":"users.yaml"}`, `{"development_actor":"local"}`} {
		cfg, path := configurationFixture(t)
		raw, _ := json.Marshal(cfg)
		var document map[string]any
		json.Unmarshal(raw, &document)
		document["auth"] = json.RawMessage(old)
		raw, _ = json.Marshal(document)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("retired authentication accepted")
		}
	}
}

func TestEvolutionSpaceConfigurationHasNoRetiredAlias(t *testing.T) {
	cfg, path := configurationFixture(t)
	cfg.EvolutionSpace = &ParticipantConnectionConfig{Endpoint: "https://space.example.com", CredentialEnv: "EVOLUTION_SPACE_CENTRAL_SECRET"}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.EvolutionSpace == nil || *loaded.EvolutionSpace != *cfg.EvolutionSpace {
		t.Fatal("new application configuration unavailable", err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["evaluation_space"] = document["evolution_space"]
	delete(document, "evolution_space")
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("retired application configuration accepted")
	}
}
