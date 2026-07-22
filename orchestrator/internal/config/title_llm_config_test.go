package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTitleLLMValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "title-llm.yaml")
	content := `enabled: true
base_url: "  https://api.example.com/openai/v1  "
api_key: "  sk-secret  "
model: "  glm-5.2  "
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadTitleLLM(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Enabled {
		t.Fatalf("expected enabled")
	}
	if cfg.BaseURL != "https://api.example.com/openai/v1" {
		t.Fatalf("base_url not trimmed: %q", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-secret" {
		t.Fatalf("api_key not trimmed: %q", cfg.APIKey)
	}
	if cfg.Model != "glm-5.2" {
		t.Fatalf("model not trimmed: %q", cfg.Model)
	}
}

func TestLoadTitleLLMMissingFileIsDisabled(t *testing.T) {
	cfg, err := loadTitleLLM(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg != (TitleLLMConfig{}) {
		t.Fatalf("expected zero-value config, got %+v", cfg)
	}
}

func TestLoadTitleLLMEmptyFileIsDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "title-llm.yaml")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadTitleLLM(path)
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if cfg != (TitleLLMConfig{}) {
		t.Fatalf("expected zero-value config, got %+v", cfg)
	}
}

func TestLoadTitleLLMRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "title-llm.yaml")
	content := `enabled: true
model: glm-5.2
temperature: 0.5
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadTitleLLM(path); err == nil {
		t.Fatalf("expected error for unknown field")
	}
}
