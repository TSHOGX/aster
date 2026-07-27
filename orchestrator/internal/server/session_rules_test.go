package server

import (
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// TestValidateNewSessionRejectsBadInput proves the domain admission rules that
// moved out of store.CreateSession are still enforced, now on the caller side.
func TestValidateNewSessionRejectsBadInput(t *testing.T) {
	valid := store.Session{
		ID:     "sess_ok",
		UserID: "lab",
		Status: string(sessionstate.Created),
		Agent:  "pi",
	}
	if err := validateNewSession(valid); err != nil {
		t.Fatalf("valid session rejected: %v", err)
	}

	for _, driver := range []string{"claude", "unknown", ""} {
		session := valid
		session.Agent = driver
		err := validateNewSession(session)
		if err == nil || !strings.Contains(err.Error(), "unsupported driver") {
			t.Fatalf("driver %q: want unsupported driver error, got %v", driver, err)
		}
	}

	for _, status := range []string{"bogus_status", "not_a_state", ""} {
		session := valid
		session.Status = status
		if err := validateNewSession(session); err == nil {
			t.Fatalf("status %q: want rejection, got nil", status)
		}
	}
}
