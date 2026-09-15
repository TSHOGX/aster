package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitMissingNeverFallsBack(t *testing.T) {
	if _, e := LoadFile(filepath.Join(t.TempDir(), "missing.yaml")); e == nil {
		t.Fatal("explicit missing config accepted")
	}
}
func TestExplicitConfigIndependentOfCWD(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "harness.yaml")
	if e := os.WriteFile(p, []byte("harness:\n  run_dir: /var/lib/test-harness/run\n"), 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := LoadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.AuthUsersPath != filepath.Join(d, "auth-users.yaml") {
		t.Fatalf("auth path %s", cfg.AuthUsersPath)
	}
}
func TestManagedRequiresCredentials(t *testing.T) {
	t.Setenv("HARNESS_MANAGED", "1")
	t.Setenv("HARNESS_AUTH_USERS_PATH", filepath.Join(t.TempDir(), "missing-auth.yaml"))
	p := filepath.Join(t.TempDir(), "harness.yaml")
	os.WriteFile(p, []byte("harness:\n  run_dir: /var/lib/test-harness/run\n"), 0600)
	if _, e := LoadFile(p); e == nil {
		t.Fatal("managed open mode accepted")
	}
}
