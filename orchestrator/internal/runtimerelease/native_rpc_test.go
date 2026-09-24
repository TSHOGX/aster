package runtimerelease

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedNativeRPCPatch(t *testing.T) {
	original, err := os.ReadFile("testdata/pi-rpc-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patchNativeRPC(original)
	if err != nil {
		t.Fatal(err)
	}
	again, err := patchNativeRPC(patched)
	if err != nil || !bytes.Equal(patched, again) {
		t.Fatal("patch is not idempotent", err)
	}
	changed := append(append([]byte{}, original...), '\n')
	if _, err = patchNativeRPC(changed); err == nil {
		t.Fatal("unreviewed native source accepted")
	}
}

func TestNativeSettlementCapabilityCannotBeDeclaredWithoutPatchedBytes(t *testing.T) {
	root, manifest, _ := releaseFixture(t)
	manifest.NativeProtocol = 0
	raw, _ := Bytes(manifest)
	digest, _ := Digest(raw)
	if _, err := Load(bytes.NewReader(raw), digest); err == nil {
		t.Fatal("missing native settlement capability accepted")
	}
	manifest.NativeProtocol = NativeProtocol
	manifest.Version = 2
	raw, _ = Bytes(manifest)
	digest, _ = Digest(raw)
	if _, err := Load(bytes.NewReader(raw), digest); err == nil {
		t.Fatal("old runtime format accepted")
	}
	manifest.Version = 3
	original, err := os.ReadFile("testdata/pi-rpc-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, strings.TrimPrefix(NativeRPCPath, "/")), original, 0644); err != nil {
		t.Fatal(err)
	}
	// Even a manifest that hashes the unpatched tree must not claim protocol 2.
	manifest.NativeTree, err = nativeTree(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = Bytes(manifest)
	digest, _ = Digest(raw)
	if err = os.WriteFile(filepath.Join(root, strings.TrimPrefix(ManifestPath, "/")), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err = Verify(root, digest); err == nil {
		t.Fatal("unpatched native RPC accepted under a matching tree digest")
	}
	if err = PatchNativeRPC(root); err == nil {
		t.Fatal("existing immutable release was modified")
	}
	if err = PatchNativeRPC("/"); err == nil {
		t.Fatal("host root accepted for patching")
	}
}
