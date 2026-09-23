package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
)

func TestRetainedManifestMustMeetCurrentHostContract(t *testing.T) {
	cfg, _ := releaseConfiguration(t)
	initial, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manifest := cfg.Manifest
	manifest.Files = map[string]string{}
	for k, v := range cfg.Manifest.Files {
		manifest.Files[k] = v
	}
	manifest.Files["/usr/local/bin/runtime-cleanup"] = "sha256:" + strings.Repeat("b", 64)
	path := filepath.Join(filepath.Dir(cfg.Template.Manifest), "retained.json")
	write := func() {
		raw, err := runtimerelease.Bytes(manifest)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := runtimerelease.Digest(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cfg.RetainedTemplates = []Template{{ID: "retained", Manifest: path, Digest: digest}}
	}
	write()
	prepared, err := prepareRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.templates) != 2 || prepared.identity.CompatibilityDigest == initial.CompatibilityDigest {
		t.Fatal("retained catalog missing from compatibility contract")
	}
	if _, ok := prepared.templates[product.ReleasedTemplate{TemplateID: "retained", Digest: cfg.RetainedTemplates[0].Digest}]; !ok {
		t.Fatal("retained binding omitted")
	}
	manifest.Version = 2
	manifest.NativeProtocol = 0
	write()
	if _, err = InspectRelease(cfg); err == nil {
		t.Fatal("self-consistent old retained manifest bypassed current host requirements")
	}
	if _, err = os.Stat(cfg.StateDirectory); !os.IsNotExist(err) {
		t.Fatal("retained validation created application state", err)
	}
}

func TestRetainedCatalogRejectsAmbiguousAndUnboundedEntries(t *testing.T) {
	cfg, _ := releaseConfiguration(t)
	for _, entries := range [][]Template{{cfg.Template}, make([]Template, 65), {{ID: "old", Manifest: "relative", Digest: cfg.Template.Digest}}} {
		cfg.RetainedTemplates = entries
		if err := cfg.Validate(); err == nil {
			t.Fatal("invalid retained catalog accepted")
		}
	}
}
