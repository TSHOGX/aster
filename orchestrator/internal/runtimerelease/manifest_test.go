package runtimerelease

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) (string, Manifest, string) {
	t.Helper()
	root := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range Components {
		file := filepath.Join(root, strings.TrimPrefix(component, "/"))
		if err = os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if component == "/usr/local/share/project-runtime/system.md" {
			err = os.WriteFile(file, []byte("Project runtime instructions"), 0644)
		} else {
			err = os.Link(self, file)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	pkg := filepath.Join(root, strings.TrimPrefix(NativeRoot, "/"), PiPackage)
	if err = os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"name": PiPackage, "version": PiVersion})
	if err = os.WriteFile(filepath.Join(pkg, "package.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	types := filepath.Join(pkg, "dist/modes/rpc")
	if err = os.MkdirAll(types, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(types, "rpc-types.d.ts"), []byte(`type Command = { type: "abort" } | { type: "steer"; message: string }`), 0644); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("testdata/pi-rpc-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(types, "rpc-mode.js"), original, 0644); err != nil {
		t.Fatal(err)
	}
	if err = PatchNativeRPC(root); err != nil {
		t.Fatal(err)
	}
	manifest, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = Bytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(raw)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, strings.TrimPrefix(ManifestPath, "/"))
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, raw, 0644); err != nil {
		t.Fatal(err)
	}
	return root, manifest, digest
}
func TestManifestVerifiesActualRuntimeAndNativeBytes(t *testing.T) {
	root, _, digest := releaseFixture(t)
	if err := Verify(root, digest); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(root, strings.TrimPrefix(NativeRoot, "/"), PiPackage, "changed.js")
	if err := os.WriteFile(native, []byte("changed dependency"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root, digest); err == nil {
		t.Fatal("unrecorded native dependency accepted")
	}
	if err := os.Remove(native); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "usr/local/bin/workspace-files")
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("wrong executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root, digest); err == nil {
		t.Fatal("changed Go executable accepted")
	}
}
func TestManifestRejectsWrongPinProtocolAndEscapingSymlinks(t *testing.T) {
	root, manifest, digest := releaseFixture(t)
	raw, _ := Bytes(manifest)
	if _, err := Load(bytes.NewReader(raw), "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong release digest accepted")
	}
	manifest.AgentProtocol++
	raw, _ = Bytes(manifest)
	changed, _ := Digest(raw)
	if _, err := Load(bytes.NewReader(raw), changed); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
	manifest.AgentProtocol--
	manifest.ReaderProtocol = 0
	raw, _ = Bytes(manifest)
	changed, _ = Digest(raw)
	if _, err := Load(bytes.NewReader(raw), changed); err == nil {
		t.Fatal("runtime without reusable reader protocol accepted")
	}
	target := filepath.Join(root, "usr/local/bin/pi")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../../outside", target); err != nil {
		t.Fatal(err)
	}
	if err := Verify(root, digest); err == nil {
		t.Fatal("escaping guest symlink accepted")
	}
}

func TestNativeTreeRejectsUnhashedSymlinkTargets(t *testing.T) {
	root, _, _ := releaseFixture(t)
	target := filepath.Join(root, "outside.js")
	if err := os.WriteFile(target, []byte("unhashed executable content"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, strings.TrimPrefix(NativeRoot, "/"), "external.js")
	if err := os.Symlink("/outside.js", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root); err == nil {
		t.Fatal("native link outside hashed tree accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(PiPackage+"/package.json", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root); err != nil {
		t.Fatalf("internal dependency link rejected: %v", err)
	}
}

func TestReleaseRejectsMixedBinaryPlatforms(t *testing.T) {
	root, manifest, _ := releaseFixture(t)
	binary := filepath.Join(root, "usr/local/bin/runtime-cleanup")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture is a native little-endian ELF. Change only e_machine in a copy.
	machine := uint16(183)
	if manifest.Platform == "linux/arm64" {
		machine = 62
	}
	raw[18], raw[19] = byte(machine), byte(machine>>8)
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, raw, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root); err == nil {
		t.Fatal("mixed architecture release accepted")
	}
}
