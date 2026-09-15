package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"harness-platform/orchestrator/internal/delivery"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBundle(t *testing.T, cache string) delivery.Bundle {
	t.Helper()
	dir := filepath.Join(cache, "v1.2.3")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(dir, "payload.tar.gz")
	f, e := os.Create(archive)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if e = tw.WriteHeader(&tar.Header{Name: "file", Mode: 0644, Size: 2}); e != nil {
		t.Fatal(e)
	}
	tw.Write([]byte("ok"))
	tw.Close()
	gz.Close()
	f.Close()
	b, e := os.ReadFile(archive)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	makeArtifact := func(name string) delivery.Artifact {
		if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
		return delivery.Artifact{Name: name, Size: int64(len(b)), SHA256: hash}
	}
	m := delivery.Manifest{Format: 1, Version: "v1.2.3", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", Ubuntu: []string{"22.04", "24.04"}, Artifacts: map[string]delivery.Artifact{"backend": makeArtifact("backend.tar.gz"), "frontend": makeArtifact("frontend.tar.gz"), "deploy": makeArtifact("deploy.tar.gz")}, Sandbox: delivery.Sandbox{Reference: "ghcr.io/example/repo@sha256:" + strings.Repeat("b", 64), Archive: makeArtifact("sandbox.tar.gz"), Pi: "0.77.0", Bridge: "v1"}, Schema: map[string]int{"store": 1, "memorystore": 1}, RunscVersions: []string{"test"}, Downtime: true, Rollback: "snapshot-before-traffic"}
	if e = delivery.WriteJSON(filepath.Join(dir, "release-manifest.json"), m); e != nil {
		t.Fatal(e)
	}
	bundle, e := delivery.OpenBundle(dir)
	if e != nil {
		t.Fatal(e)
	}
	return bundle
}
func TestArtifactsNeedNeitherTargetNorHostTools(t *testing.T) {
	// An empty PATH proves artifact handling never invokes systemd, runsc or a
	// deployment hook. No target configuration exists anywhere in this test.
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	cache := t.TempDir()
	bundle := testBundle(t, cache)
	if e := run(ctx, []string{"verify", "--cache", cache, "--release", "v1.2.3"}); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(t.TempDir(), "offline.tar.gz")
	if e := run(ctx, []string{"export", "--cache", cache, "--release", "v1.2.3", "--output", output}); e != nil {
		t.Fatal(e)
	}
	dest := t.TempDir()
	if e := run(ctx, []string{"import", "--cache", dest, "--archive", output, "--manifest-sha256", bundle.Digest}); e != nil {
		t.Fatal(e)
	}
	if e := run(ctx, []string{"verify", "--cache", dest, "--release", "v1.2.3"}); e != nil {
		t.Fatal(e)
	}
}
func TestDeploymentRequiresExplicitConfig(t *testing.T) {
	if e := run(context.Background(), []string{"upgrade", "--release", "v1.2.3"}); e == nil || !strings.Contains(e.Error(), "--config") {
		t.Fatalf("unexpected result %v", e)
	}
}
func TestArtifactsRefuseToReadTargetConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "target.local.json")
	if e := run(context.Background(), []string{"verify", "--cache", t.TempDir(), "--release", "v1.2.3", "--config", p}); e == nil || !strings.Contains(e.Error(), "do not read") {
		t.Fatalf("unexpected result %v", e)
	}
}
func TestStatusHasNoNameRequirementOrSiblingDiscovery(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	os.Mkdir(state, 0700)
	target := delivery.Target{Current: filepath.Join(dir, "current"), Operations: state}
	body, _ := json.Marshal(target)
	config := filepath.Join(dir, "deploy.local.json")
	os.WriteFile(config, body, 0600)
	// A malformed adjacent inventory must not affect an explicitly selected file.
	sibling := filepath.Join(dir, "unrelated")
	os.Mkdir(sibling, 0700)
	os.WriteFile(filepath.Join(sibling, "deploy.json"), []byte("not json"), 0600)
	if e := run(context.Background(), []string{"status", "--config", config}); e != nil {
		t.Fatal(e)
	}
}
