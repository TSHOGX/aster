package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOverride drops a prompt override file in a temp dir and points
// PromptsPathEnv at it.
func writeOverride(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kb-prompts.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	t.Setenv(PromptsPathEnv, path)
	return path
}

// The committed defaults must be complete on their own: releases ship the binary
// without config/, so an embedded set with a hole would leave every install
// unable to start the loop.
func TestEmbeddedPromptsComplete(t *testing.T) {
	p, err := EmbeddedPrompts()
	if err != nil {
		t.Fatalf("EmbeddedPrompts: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("embedded prompts incomplete: %v", err)
	}
	for _, got := range []string{
		p.FactsPrompt(), p.FailurePrompt(), p.SkillPrompt(), p.ReflectPrompt(),
		p.BlockKeyPrompt(), p.ClusterPrompt(), p.ReducePrompt(), p.KBDedupPrompt(),
		p.VerifyPrompt(), p.MemoryCuratePrompt(), p.SkillCriticPrompt(), p.CriticPrompt(),
	} {
		if strings.TrimSpace(got) == "" {
			t.Error("composed prompt is empty")
		}
	}
}

func TestLoadPromptsWithoutEnvUsesEmbedded(t *testing.T) {
	t.Setenv(PromptsPathEnv, "")
	got, err := LoadPrompts()
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	want, err := EmbeddedPrompts()
	if err != nil {
		t.Fatalf("EmbeddedPrompts: %v", err)
	}
	if got != want {
		t.Error("unset env should yield exactly the embedded set")
	}
}

func TestLoadPromptsOverridePrecedence(t *testing.T) {
	embedded, err := EmbeddedPrompts()
	if err != nil {
		t.Fatalf("EmbeddedPrompts: %v", err)
	}
	writeOverride(t, "verify: |-\n  只核验，不要写入。\n")
	got, err := LoadPrompts()
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	if got.Verify != "只核验，不要写入。" {
		t.Errorf("verify not overridden: %q", got.Verify)
	}
	if got.BlockKey != embedded.BlockKey || got.Critic != embedded.Critic {
		t.Error("untouched keys should keep their embedded bodies")
	}
	if !strings.HasPrefix(got.VerifyPrompt(), embedded.Grounding) {
		t.Error("overridden prompt should still be composed with grounding")
	}
}

// Overriding `grounding` alone must cascade into the eleven grounded prompts
// while leaving Reflect untouched — that asymmetry is the reason grounding is a
// separate key rather than being baked into each body.
func TestGroundingCascades(t *testing.T) {
	embedded, err := EmbeddedPrompts()
	if err != nil {
		t.Fatalf("EmbeddedPrompts: %v", err)
	}
	writeOverride(t, "grounding: |-\n  只依据记录，禁止臆测。\n")
	got, err := LoadPrompts()
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	for name, prompt := range map[string]string{
		"facts":  got.FactsPrompt(),
		"verify": got.VerifyPrompt(),
		"critic": got.CriticPrompt(),
	} {
		if !strings.HasPrefix(prompt, "只依据记录，禁止臆测。") {
			t.Errorf("%s did not pick up the new grounding clause", name)
		}
	}
	if got.ReflectPrompt() != embedded.Reflect {
		t.Error("reflect must not gain a grounding prefix")
	}
}

func TestLoadPromptsRefusesUnknownKey(t *testing.T) {
	writeOverride(t, "verrify: |-\n  typo\n")
	if _, err := LoadPrompts(); err == nil {
		t.Fatal("expected an error for an unknown prompt key")
	} else if !strings.Contains(err.Error(), "verrify") {
		t.Errorf("error should name the offending key: %v", err)
	}
}

// An empty body is a typo, not a request for the default: accepting it would
// send a blank system prompt to the model.
func TestLoadPromptsRefusesEmptyValue(t *testing.T) {
	writeOverride(t, "verify: \"\"\n")
	if _, err := LoadPrompts(); err == nil {
		t.Fatal("expected an error for an empty prompt body")
	}
}

func TestLoadPromptsRefusesUnusableFile(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv(PromptsPathEnv, filepath.Join(t.TempDir(), "absent.yaml"))
		if _, err := LoadPrompts(); err == nil {
			t.Fatal("expected an error for a configured-but-missing file")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		writeOverride(t, "verify: [not, a, string]\n")
		if _, err := LoadPrompts(); err == nil {
			t.Fatal("expected an error for a malformed override")
		}
	})
}
