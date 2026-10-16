package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

func TestNativeRegistryRejectsUnregisteredInputsAndLoadsExamples(t *testing.T) {
	store, err := projections.Open(context.Background(), filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Setenv("NATIVE_TEST_SECRET", strings.Repeat("s", 32))
	path := filepath.Join(t.TempDir(), "native.json")
	model := nativeModelConfig{Reference: evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}, Endpoint: "https://model.example", ClientID: "space", SecretEnv: "NATIVE_TEST_SECRET", Ceiling: projections.RunUsage{Tokens: 100, CostMicros: 100}}
	for _, mode := range []string{"valid", "origin", "credential", "budget", "schema", "duplicate", "tool-invalid", "tool-duplicate", "tool-limit"} {
		t.Run(mode, func(t *testing.T) {
			config := nativeConfig{Models: []nativeModelConfig{model}, Schemas: map[string]json.RawMessage{}, ExampleBuilders: true}
			tool := evolution.Revision{ID: "lookup", Revision: 1, Digest: evolution.Digest([]byte("lookup"))}
			config.Models[0].Tools = []evolution.Revision{tool}
			switch mode {
			case "tool-invalid":
				config.Models[0].Tools[0].Digest = "invalid"
			case "tool-duplicate":
				config.Models[0].Tools = append(config.Models[0].Tools, tool)
			case "tool-limit":
				config.Models[0].Tools = make([]evolution.Revision, 129)
			case "origin":
				config.Models[0].Endpoint = "http://remote.example"
			case "credential":
				config.Models[0].SecretEnv = "UNSET_NATIVE_SECRET"
			case "budget":
				config.Models[0].Ceiling.Tokens = 0
			case "schema":
				config.Schemas[evolution.Digest([]byte("wrong"))] = json.RawMessage(`{}`)
			case "duplicate":
				config.Models = append(config.Models, model)
			}
			raw, _ := json.Marshal(config)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			registry, close, err := readNativeRegistry(path, store)
			if mode != "valid" {
				if err == nil {
					close()
					t.Fatal("invalid native registry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			node := evolution.Node{Kind: "agent", ModelRef: &model.Reference, Tools: []evolution.Revision{tool}}
			if err = registry.Check(context.Background(), node); err != nil {
				t.Fatal("registered tool rejected", err)
			}
			node.Tools[0].Revision++
			if err = registry.Check(context.Background(), node); err == nil {
				t.Fatal("unregistered tool revision accepted")
			}
			if len(registry.Models) != 1 || len(registry.Transforms) != 3 || len(registry.Schemas) < 3 {
				t.Fatal("example registry incomplete")
			}
		})
	}
}
