package agentrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionConfigurationSelectionSurvivesCredentialReplacement(t *testing.T) {
	source, root := t.TempDir(), t.TempDir()
	authored := `{"providers":{"local":{"apiKey":"harness-placeholder","baseUrl":"http://service"}}}`
	if err := os.WriteFile(filepath.Join(source, "models.json"), []byte(authored), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"defaultProvider":"local"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materializeConfig(source, root); err != nil {
		t.Fatal(err)
	}
	if err := materializeCredential(root, "first-incarnation-token"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "models.json"), []byte(`{"different":"configuration"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materializeConfig(source, root); err != nil {
		t.Fatal(err)
	}
	if err := materializeCredential(root, "second-incarnation-token"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "second-incarnation-token") || strings.Contains(string(raw), "first-incarnation-token") || strings.Contains(string(raw), "different") {
		t.Fatal(string(raw))
	}
	raw, err = os.ReadFile(filepath.Join(root, "authored-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != authored {
		t.Fatal("original authored selection was overwritten")
	}
}
