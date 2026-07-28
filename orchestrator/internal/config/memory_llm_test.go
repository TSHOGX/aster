package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMemoryLLM writes a memory-llm.yaml body to a temp file and returns its path.
func writeMemoryLLM(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory-llm.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write memory-llm.yaml: %v", err)
	}
	return path
}

// The publish gate must default to closed: a config file that says nothing about
// auto_publish must NOT let distilled knowledge reach sandboxes unreviewed.
func TestMemoryLLMAutoPublishDefaultsFalse(t *testing.T) {
	path := writeMemoryLLM(t, "enabled: true\nbase_url: https://x/v1\napi_key: k\nmodel: glm-5.2\n")
	cfg, err := loadMemoryLLM(path)
	if err != nil {
		t.Fatalf("loadMemoryLLM: %v", err)
	}
	if cfg.AutoPublish {
		t.Fatal("auto_publish must default to false when unset")
	}
	if cfg.Agentic {
		t.Fatal("agentic must default to false when unset")
	}
}

// The scheduled wrapper exports HARNESS_MEMORY_AUTO_PUBLISH=false unconditionally,
// so the env override must win over a file that opted into auto-publishing.
func TestMemoryLLMAutoPublishEnvOverridesFile(t *testing.T) {
	path := writeMemoryLLM(t, "enabled: true\nbase_url: https://x/v1\napi_key: k\nmodel: glm-5.2\nauto_publish: true\n")

	cfg, err := loadMemoryLLM(path)
	if err != nil {
		t.Fatalf("loadMemoryLLM: %v", err)
	}
	if !cfg.AutoPublish {
		t.Fatal("auto_publish: true in the file should parse as true")
	}

	t.Setenv("HARNESS_MEMORY_AUTO_PUBLISH", "false")
	cfg, err = loadMemoryLLM(path)
	if err != nil {
		t.Fatalf("loadMemoryLLM with env: %v", err)
	}
	if cfg.AutoPublish {
		t.Fatal("HARNESS_MEMORY_AUTO_PUBLISH=false must override auto_publish: true")
	}
}
