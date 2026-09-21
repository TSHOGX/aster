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
	for _, token := range []string{"runtime_generations", "workspace_fence_", "auto_dispatch", "shell_pty", "session_provisioning"} {
		if strings.Contains(string(raw), token) {
			t.Fatalf("retired schema contract remains: %s", token)
		}
	}
}
