package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
)

func TestVolumePublicationRequiresExactReviewedContentAndCleanRuntimeDeclaration(t *testing.T) {
	source := t.TempDir()
	file := filepath.Join(source, "example.txt")
	if err := os.WriteFile(file, []byte("reviewed example"), 0600); err != nil {
		t.Fatal(err)
	}
	revision, err := content.Read(source)
	if err != nil {
		t.Fatal(err)
	}
	input := VolumePublication{Reference: product.ArtifactReference{ID: "examples", Version: "v1", Digest: revision.Digest}, Source: source, CapacityBytes: 8 << 20, MinimumBytes: 1 << 20, Roles: []string{"data", "workspace"}}
	artifact, measured, err := input.prepare("store")
	if err != nil || measured.Digest != revision.Digest || artifact.StoreID != "store" || artifact.PublicationPurpose != "builtin" {
		t.Fatal(artifact, err)
	}
	for _, change := range []func(*VolumePublication){
		func(p *VolumePublication) { p.Reference.Digest = "sha256:" + strings.Repeat("a", 64) },
		func(p *VolumePublication) { p.Roles = []string{"runtime"} },
		func(p *VolumePublication) { p.Roles = []string{"data", "data"} },
		func(p *VolumePublication) { p.MinimumBytes = 1 },
		func(p *VolumePublication) { p.CapacityBytes = 1 },
		func(p *VolumePublication) { p.Source = "relative" },
	} {
		invalid := input
		change(&invalid)
		if _, _, err = invalid.prepare("store"); err == nil {
			t.Fatal("invalid declaration accepted", invalid)
		}
	}
	input.Roles = []string{"runtime"}
	input.CleanRuntime = true
	if _, _, err = input.prepare("store"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("changed after review"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = input.prepare("store"); err == nil {
		t.Fatal("changed source accepted")
	}
}
