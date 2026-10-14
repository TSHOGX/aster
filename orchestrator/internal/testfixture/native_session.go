package testfixture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/runtimerelease"
)

// NativeSessionModule builds the pinned runtime files into a disposable image
// root. Native continuation tests execute these bytes without changing npm files.
func NativeSessionModule(t *testing.T) string {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	fixtures := filepath.Join(filepath.Dir(source), "../runtimerelease/testdata")
	root := t.TempDir()
	for path, name := range map[string]string{
		runtimerelease.NativeRPCPath:     "pi-rpc-0.77.0.js",
		runtimerelease.NativeSessionPath: "pi-session-0.77.0.js",
		runtimerelease.NativeRoot + "/" + runtimerelease.PiPackage + "/dist/index.js":                  "pi-index-0.77.0.js",
		runtimerelease.NativeRoot + "/" + runtimerelease.PiPackage + "/dist/index.d.ts":                "pi-index-0.77.0.d.ts",
		runtimerelease.NativeRoot + "/" + runtimerelease.PiPackage + "/dist/core/session-manager.d.ts": "pi-session-0.77.0.d.ts",
	} {
		raw, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(root, strings.TrimPrefix(path, "/"))
		if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtimerelease.PatchNativeRPC(root); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, strings.TrimPrefix(runtimerelease.NativeSessionPath, "/"))
}
