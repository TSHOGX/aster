package runtimerelease

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionFixtureMatchesRuntimePin(t *testing.T) {
	raw, err := os.ReadFile("package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct{ Version, Integrity string }
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{PiPackage, "@earendil-works/pi-ai", "@earendil-works/pi-agent-core", "@earendil-works/pi-tui"} {
		dependency := lock.Packages["node_modules/"+name]
		if dependency.Version != PiVersion || (name == PiPackage && dependency.Integrity != PiIntegrity) {
			t.Fatalf("Session fixture dependency %s differs from the runtime pin", name)
		}
	}
}

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
	if _, err := exec.LookPath("node"); err == nil {
		check := exec.Command("node", "--check", "--input-type=module")
		check.Stdin = bytes.NewReader(patched)
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("patched RPC is invalid JavaScript: %v\n%s", err, output)
		}
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
	manifest.Version = 5
	original, err := os.ReadFile("testdata/pi-rpc-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, strings.TrimPrefix(NativeRPCPath, "/")), original, 0644); err != nil {
		t.Fatal(err)
	}
	// Even a manifest that hashes the unpatched tree must not claim the current protocol.
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

// Execute the actual patched RPC function with deterministic Session/transport
// doubles. Full AgentSession provider, extension and crash qualification remains
// separate from these handler-race tests.
func TestPatchedRPCControlContract(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable")
	}
	original, err := os.ReadFile("testdata/pi-rpc-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patchNativeRPC(original)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "rpc.mjs")
	if err = os.WriteFile(file, patched, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("node", "testdata/rpc-contract.test.mjs", file)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("RPC contract: %v\n%s", err, output)
	}
}

func TestPatchedRPCWithFullSession(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable")
	}
	original, err := os.ReadFile("testdata/pi-rpc-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patchNativeRPC(original)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "rpc.mjs")
	if err = os.WriteFile(file, patched, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("node", "testdata/rpc-session.test.mjs", file)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("full Session RPC: %v\n%s", err, output)
	}
}
