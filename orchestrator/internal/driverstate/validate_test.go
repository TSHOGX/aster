package driverstate

import (
	"context"
	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiDriverStateValidation(t *testing.T) {
	payload, digest, err := store.CanonicalDriverStatePayload(map[string]any{"schema_version": 1, "driver_id": "pi", "state_kind": "pi_uninitialized", "session_dir": agents.PiSessionDir}, "pi")
	if err != nil {
		t.Fatalf("bootstrap pi state: %v", err)
	}
	if digest != store.DriverStateDigest(payload) {
		t.Fatalf("bootstrap digest mismatch")
	}
	if _, err := ValidateLocal(context.Background(), "pi", payload, "", ""); err == nil || !strings.Contains(err.Error(), "host path is required") {
		t.Fatalf("expected missing pi host path rejection, got %v", err)
	}
	initialized := map[string]any{
		"schema_version":           1,
		"driver_id":                "pi",
		"state_kind":               "pi_session",
		"session_dir":              "/agent-home/.pi/agent/sessions",
		"selected_session_relpath": "session-1.jsonl",
		"selected_session_file":    "/agent-home/.pi/agent/sessions/session-1.jsonl",
		"selected_session_id":      "pi-session-1",
		"last_completed_turn_id":   "42",
	}
	if _, _, err := store.CanonicalDriverStatePayload(initialized, "pi"); err != nil {
		t.Fatalf("initialized pi state rejected: %v", err)
	}

	for _, rel := range []string{"../escape.jsonl", "/tmp/session.jsonl", "nested/../escape.jsonl", ""} {
		initialized["selected_session_relpath"] = rel
		initialized["selected_session_file"] = "/agent-home/.pi/agent/sessions/" + rel
		if _, _, err := store.CanonicalDriverStatePayload(initialized, "pi"); err == nil {
			t.Fatalf("pi state relpath %q should be rejected", rel)
		}
	}

	agentHome := t.TempDir()
	sessionRoot := filepath.Join(agentHome, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("create pi session root: %v", err)
	}
	sessionFile := filepath.Join(sessionRoot, "session-1.jsonl")
	if err := os.WriteFile(sessionFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write pi session file: %v", err)
	}
	initialized["selected_session_relpath"] = "session-1.jsonl"
	initialized["selected_session_file"] = agents.PiSessionDir + "/session-1.jsonl"
	canonical, _, err := store.CanonicalDriverStatePayload(initialized, "pi")
	if err != nil {
		t.Fatalf("canonical pi host state: %v", err)
	}
	if _, err := ValidateLocal(context.Background(), "pi", canonical, agentHome, "42"); err != nil {
		t.Fatalf("host pi state rejected valid session file: %v", err)
	}
	if err := os.Remove(sessionFile); err != nil {
		t.Fatalf("remove pi session file: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "escape.jsonl"), sessionFile); err != nil {
		t.Fatalf("symlink pi session file: %v", err)
	}
	if _, err := ValidateLocal(context.Background(), "pi", canonical, agentHome, "42"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink session file rejection, got %v", err)
	}

	symlinkHome := t.TempDir()
	outsideRoot := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("create external pi session root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, "session-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write external pi session file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(symlinkHome, ".pi", "agent"), 0o755); err != nil {
		t.Fatalf("create pi agent dir: %v", err)
	}
	if err := os.Symlink(outsideRoot, filepath.Join(symlinkHome, ".pi", "agent", "sessions")); err != nil {
		t.Fatalf("symlink pi session root: %v", err)
	}
	if _, err := ValidateLocal(context.Background(), "pi", canonical, symlinkHome, "42"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink session root rejection, got %v", err)
	}
}
