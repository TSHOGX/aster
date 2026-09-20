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

func TestRemovedConfigurationIsRejected(t *testing.T) {
	for _, key := range []string{"network", "checkpoint", "runtime", "sandbox_identity", "model_proxy", "data_proxies", "runtime_providers"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "harness.yaml")
			if err := os.WriteFile(path, []byte("harness:\n  "+key+": {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(path); err == nil {
				t.Fatalf("removed setting %s accepted", key)
			}
		})
	}
}

func TestProductWorkerBoundsRemainValidated(t *testing.T) {
	for _, body := range []string{
		"scheduler:\n    worker_count: 0", "scheduler:\n    lease_ttl: 0s", "scheduler:\n    max_retries: -1", "scheduler:\n    max_pending_runs_per_schedule: 0",
		"bridge:\n    turn_resume:\n      enabled: true\n      max_attempts: 0", "bridge:\n    turn_resume:\n      backoff: -1s",
	} {
		p := filepath.Join(t.TempDir(), "harness.yaml")
		if err := os.WriteFile(p, []byte("harness:\n  "+body+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFile(p); err == nil {
			t.Fatalf("invalid worker configuration accepted: %s", body)
		}
	}
}
