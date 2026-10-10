package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
)

func releaseConfiguration(t *testing.T) (Config, string) {
	t.Helper()
	cfg, path := configurationFixture(t)
	directory := filepath.Join(filepath.Dir(path), "reviewed")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "context.md")
	if err := os.WriteFile(file, []byte("private reviewed fixture text"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Content.SharedAssets = "reviewed"
	cfg.Titles = TitleService{BaseURL: "https://model.example.invalid", Model: "fixture", APIKeyEnv: "FIXTURE_SECRET"}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, file
}

func TestCandidateIdentityIsReadOnlyDeterministicAndContainsNoConfiguration(t *testing.T) {
	cfg, file := releaseConfiguration(t)
	t.Setenv("FIXTURE_SECRET", "private-provider-key")
	first, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("identical candidate inputs changed identity", first, second)
	}
	if _, err = os.Stat(cfg.StateDirectory); !os.IsNotExist(err) {
		t.Fatal("inspection created application state", err)
	}
	version, digest, err := product.SchemaIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != version || first.SchemaDigest != digest || first.RuntimeDigest != cfg.Template.Digest {
		t.Fatal(first)
	}
	for _, value := range []string{first.ExecutableDigest, first.CompatibilityDigest, first.ConfigurationDigest, first.ContentDigests["assets"]} {
		if !content.ValidDigest(value) {
			t.Fatal("identity missing measured digest", first)
		}
	}
	raw, _ := json.Marshal(first)
	for _, private := range []string{file, cfg.StateDirectory, cfg.Titles.BaseURL, "private reviewed fixture text", "private-provider-key", "FIXTURE_SECRET"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("candidate inspection exposed configuration or content")
		}
	}

}

func TestCandidateCompatibilityTracksConfigurationAndReviewedBytes(t *testing.T) {
	cfg, file := releaseConfiguration(t)
	prepared, err := prepareRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := prepared.identity
	cfg.Limits.Agents++
	changed, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed.CompatibilityDigest == original.CompatibilityDigest || changed.ConfigurationDigest == original.ConfigurationDigest {
		t.Fatal("capacity/configuration change omitted from contract")
	}
	cfg.Limits.Agents--
	if err = os.WriteFile(file, []byte("changed reviewed fixture text"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err = InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed.CompatibilityDigest == original.CompatibilityDigest || changed.ContentDigests["assets"] == original.ContentDigests["assets"] {
		t.Fatal("reviewed content change omitted from contract")
	}
	if changed.ConfigurationDigest != original.ConfigurationDigest {
		t.Fatal("authored bytes are not separate from configuration")
	}
	if len(prepared.publications) != 1 || prepared.publications[0].revision.Digest != original.ContentDigests["assets"] {
		t.Fatal("startup lost its measured publication revision")
	}
	// The publisher validates reads against this original revision. Startup does
	// not reread and silently replace it with the source tree's newer identity.
	if prepared.publications[0].revision.Digest == changed.ContentDigests["assets"] {
		t.Fatal("prepared revision followed mutable source")
	}
	if err = os.WriteFile(cfg.Template.Manifest, []byte("invalid manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = InspectRelease(cfg); err == nil {
		t.Fatal("candidate inspection accepted a changed guest manifest")
	}
}

func TestCandidateCompatibilityIncludesNotificationPolicy(t *testing.T) {
	cfg, _ := releaseConfiguration(t)
	original, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*NotificationConfig){
		func(c *NotificationConfig) { c.AgentRunOutcome = !c.AgentRunOutcome },
		func(c *NotificationConfig) { c.ConversationShared = !c.ConversationShared },
		func(c *NotificationConfig) { c.OperatorDirected = !c.OperatorDirected },
		func(c *NotificationConfig) { c.OperatorBroadcast = !c.OperatorBroadcast },
	} {
		candidate := cfg
		change(&candidate.Notifications)
		changed, err := InspectRelease(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if changed.ConfigurationDigest == original.ConfigurationDigest || changed.CompatibilityDigest == original.CompatibilityDigest {
			t.Fatal("notification policy omitted from fixed release contract", candidate.Notifications)
		}
		if changed.SchemaDigest != original.SchemaDigest {
			t.Fatal("configuration changed schema identity")
		}
	}
}
