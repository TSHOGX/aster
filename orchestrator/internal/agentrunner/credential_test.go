package agentrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func catalog(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(root, "models.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func providers(t *testing.T, root string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	if err = json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed.Providers
}

func TestCredentialReplacesPlaceholderAndPreservesCatalog(t *testing.T) {
	root := catalog(t, `{"providers":{"local":{"baseUrl":"http://model.internal:443","api":"anthropic-messages","apiKey":"harness-placeholder","models":[{"id":"m"}]},"second":{"apiKey":"harness-placeholder"}},"other":{"kept":true}}`)
	if err := materializeCredential(root, "grant.secret"); err != nil {
		t.Fatal(err)
	}
	got := providers(t, root)
	for name, provider := range got {
		if provider["apiKey"] != "grant.secret" {
			t.Fatal(name, provider)
		}
	}
	if got["local"]["baseUrl"] != "http://model.internal:443" || got["local"]["api"] != "anthropic-messages" {
		t.Fatal("provider fields were dropped", got["local"])
	}
	raw, err := os.ReadFile(filepath.Join(root, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kept":true`) {
		t.Fatal("unrelated catalog content was dropped", string(raw))
	}
	// Re-materializing under a replacement incarnation is normal: the previous
	// value is this runner's own token, not authored configuration.
	if err = materializeCredential(root, "grant.next"); err == nil {
		t.Fatal("a replacement must not silently overwrite a live credential")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatal("temporary file was left behind", entries)
	}
}

func TestCredentialRefusesAuthoredSecretsAndInvalidInput(t *testing.T) {
	// An authored catalog that already holds a real secret is a configuration
	// defect: product configuration must never carry an upstream credential.
	root := catalog(t, `{"providers":{"local":{"apiKey":"sk-real-upstream-secret"}}}`)
	if err := materializeCredential(root, "grant.secret"); err == nil {
		t.Fatal("authored secret was accepted")
	}
	for name, body := range map[string]string{
		"no providers":   `{"providers":{}}`,
		"invalid JSON":   `{`,
		"provider array": `{"providers":[]}`,
		"bad apiKey":     `{"providers":{"local":{"apiKey":7}}}`,
	} {
		if err := materializeCredential(catalog(t, body), "grant.secret"); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	for _, credential := range []string{"has\nnewline", "has\x00nul", `has"quote`, strings.Repeat("x", 257)} {
		if err := materializeCredential(catalog(t, `{"providers":{"local":{"apiKey":"harness-placeholder"}}}`), credential); err == nil {
			t.Fatalf("invalid credential %q was accepted", credential)
		}
	}
}

func TestCredentialIsSkippedWithoutServiceOrCatalog(t *testing.T) {
	// A zero-gateway deployment issues no credential and must leave the authored
	// catalog exactly as it shipped.
	root := catalog(t, `{"providers":{"local":{"apiKey":"harness-placeholder"}}}`)
	if err := materializeCredential(root, ""); err != nil {
		t.Fatal(err)
	}
	if providers(t, root)["local"]["apiKey"] != credentialPlaceholder {
		t.Fatal("no-service deployment rewrote authored configuration")
	}
	// A Session with no authored catalog simply reaches no upstream; that is not a
	// runner failure, so the Session still starts.
	if err := materializeCredential(catalog(t, ""), "grant.secret"); err != nil {
		t.Fatal(err)
	}
}
