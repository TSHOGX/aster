package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredOwnershipAndProtocolPackagesAreAbsent(t *testing.T) {
	root := moduleRoot(t)
	for _, name := range []string{"store", "server", "execution", "bridge", "agentbridge", "driverstate", "egress", "gateway", "launchplan", "nativecapture", "retainedapp", "sessionfiles", "sessionstate", "trajectory", "workspace"} {
		if _, err := os.Stat(filepath.Join(root, "internal", name)); !os.IsNotExist(err) {
			t.Fatalf("retired ownership package remains: %s", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal/product/schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"runtime_generations", "workspace_fence_", "auto_dispatch", "shell_pty", "session_provisioning", "workspace_reader_retirements", "ALTER TABLE", "DROP TABLE", "DROP TRIGGER"} {
		if strings.Contains(string(raw), token) {
			t.Fatalf("retired schema contract remains: %s", token)
		}
	}
	steps, err := filepath.Glob(filepath.Join(root, "internal/product/*_schema.sql"))
	if err != nil || len(steps) != 0 {
		t.Fatalf("historical schema steps remain: %v %v", steps, err)
	}
	for _, name := range []string{"migrations.go", "migrations_test.go"} {
		if _, err := os.Stat(filepath.Join(root, "internal/product", name)); !os.IsNotExist(err) {
			t.Fatalf("migration path remains: %s", name)
		}
	}
}
