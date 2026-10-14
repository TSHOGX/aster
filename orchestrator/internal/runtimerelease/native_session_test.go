package runtimerelease

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func patchedSessionModule(t *testing.T) string {
	t.Helper()
	original, err := os.ReadFile("testdata/pi-session-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patchNativeSession(original)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := filepath.Join(root, "dist/core/session-manager.js")
	if err = os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, patched, 0600); err != nil {
		t.Fatal(err)
	}
	for _, companion := range nativeSessionCompanions {
		raw, err := os.ReadFile("testdata/" + companion.fixture)
		if err != nil {
			t.Fatal(err)
		}
		patched, err := patchNativeSessionCompanion(raw, companion.original, companion.patched)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, companion.suffix)
		if err = os.WriteFile(path, patched, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return file
}
func TestPinnedNativeSessionPatch(t *testing.T) {
	original, err := os.ReadFile("testdata/pi-session-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patchNativeSession(original)
	if err != nil {
		t.Fatal(err)
	}
	again, err := patchNativeSession(patched)
	if err != nil || !bytes.Equal(again, patched) {
		t.Fatal("non-idempotent Session patch", err)
	}
	if _, err = patchNativeSession(append(original, '\n')); err == nil {
		t.Fatal("unknown Session source accepted")
	}
	for _, name := range []string{"migrateV1ToV2", "migrateV2ToV3", "migrateSessionEntries", "migrateToCurrentVersion"} {
		if strings.Contains(string(patched), name) {
			t.Fatal("historical Session implementation remains", name)
		}
	}
}
func TestNativeSessionV1Persistence(t *testing.T) {
	module := patchedSessionModule(t)
	command := exec.Command("node", "--import", "./testdata/native-session-loader.mjs", "--test", "native-session.test.mjs", "full_session_identity.test.mjs", "platform-schedules.test.mjs")
	command.Env = append(os.Environ(), "ASTER_TEST_NATIVE_SESSION="+module)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native Session v1: %v\n%s", err, output)
	}
}
func TestNativeSessionCapabilityRequiresPatchedBytes(t *testing.T) {
	root, manifest, _ := releaseFixture(t)
	original, err := os.ReadFile("testdata/pi-session-0.77.0.js")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, strings.TrimPrefix(NativeSessionPath, "/")), original, 0644); err != nil {
		t.Fatal(err)
	}
	// Rehashing an unpatched dependency tree cannot claim the current capability.
	manifest.NativeTree, err = nativeTree(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Bytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, strings.TrimPrefix(ManifestPath, "/")), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err = Verify(root, digest); err == nil {
		t.Fatal("unpatched Session accepted")
	}
}
