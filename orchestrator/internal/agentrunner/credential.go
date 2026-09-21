package agentrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// credentialPlaceholder is the non-secret value authored configuration carries.
// The runner replaces it once per incarnation with this Session's application
// token. Authored configuration that already holds a real secret is rejected:
// a provider credential must never be checked into product configuration.
const credentialPlaceholder = "harness-placeholder"

// materializeCredential rewrites every provider apiKey in this Session's authored
// models.json. It runs before Pi starts, so a turn never races a configuration
// rewrite, and it writes nothing when the deployment declares no service.
//
// The written file is guest-root readable by construction. A sibling Session can
// read it; that is the documented Project trust domain, not a defect.
func materializeCredential(root, credential string) error {
	if credential == "" {
		return nil
	}
	if len(credential) > 256 || strings.ContainsAny(credential, "\r\n\x00\"\\") {
		return errors.New("invalid service credential")
	}
	path := filepath.Join(root, "models.json")
	raw, err := os.ReadFile(filepath.Join(root, "authored-models.json"))
	if errors.Is(err, os.ErrNotExist) {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		// No authored catalog means this Session was not configured for a model
		// service. That is not a runner failure; it simply reaches no upstream.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var catalog struct {
		Providers map[string]map[string]json.RawMessage `json:"providers"`
	}
	rest := map[string]json.RawMessage{}
	if err = json.Unmarshal(raw, &rest); err != nil {
		return fmt.Errorf("authored model catalog is not valid JSON: %w", err)
	}
	if err = json.Unmarshal(raw, &catalog); err != nil {
		return fmt.Errorf("authored model catalog has no provider map: %w", err)
	}
	if len(catalog.Providers) == 0 {
		return errors.New("authored model catalog declares no providers")
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	for name, provider := range catalog.Providers {
		var existing string
		if value, ok := provider["apiKey"]; ok {
			if err = json.Unmarshal(value, &existing); err != nil {
				return fmt.Errorf("provider %q has an invalid apiKey", name)
			}
		}
		if existing != "" && existing != credentialPlaceholder {
			return fmt.Errorf("provider %q already holds a credential; authored configuration must carry only the placeholder", name)
		}
		provider["apiKey"] = encoded
	}
	providers, err := json.Marshal(catalog.Providers)
	if err != nil {
		return err
	}
	rest["providers"] = providers
	updated, err := json.Marshal(rest)
	if err != nil {
		return err
	}
	// Replace atomically: Pi reads this file at startup and after a reload, and a
	// partially written catalog would fail its whole provider resolution.
	temporary := path + ".tmp"
	if err = os.WriteFile(temporary, append(updated, '\n'), 0600); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}
