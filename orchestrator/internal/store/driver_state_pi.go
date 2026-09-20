package store

import (
	"fmt"
	"strings"

	"harness-platform/orchestrator/internal/agents"
)

const (
	piDriverStateKindUninitialized = "pi_uninitialized"
	piDriverStateKindSession       = "pi_session"
)

func bootstrapPiDriverState(driverStateBootstrapContext) (any, error) {
	return map[string]any{
		"schema_version": 1,
		"driver_id":      string(agents.Pi),
		"state_kind":     piDriverStateKindUninitialized,
		"session_dir":    agents.PiSessionDir,
	}, nil
}

func validatePiDriverStatePayload(object map[string]any) error {
	sessionDir, _ := object["session_dir"].(string)
	if sessionDir != agents.PiSessionDir {
		return fmt.Errorf("pi driver state session_dir = %q", sessionDir)
	}
	switch got, _ := object["state_kind"].(string); got {
	case piDriverStateKindUninitialized:
		return nil
	case piDriverStateKindSession:
		rel := strings.TrimSpace(stringValue(object["selected_session_relpath"]))
		if rel == "" {
			return fmt.Errorf("pi driver state selected_session_relpath is required")
		}
		if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
			return fmt.Errorf("pi driver state selected_session_relpath must be relative")
		}
		if rel != strings.TrimPrefix(rel, "./") || rel != strings.TrimSpace(rel) {
			return fmt.Errorf("pi driver state selected_session_relpath must be clean")
		}
		parts := strings.Split(rel, "/")
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return fmt.Errorf("pi driver state selected_session_relpath must stay under session_dir")
			}
		}
		selectedFile, _ := object["selected_session_file"].(string)
		if selectedFile != sessionDir+"/"+rel {
			return fmt.Errorf("pi driver state selected_session_file = %q, want %q", selectedFile, sessionDir+"/"+rel)
		}
		if strings.TrimSpace(stringValue(object["selected_session_id"])) == "" {
			return fmt.Errorf("pi driver state selected_session_id is required")
		}
		if strings.TrimSpace(stringValue(object["last_completed_turn_id"])) == "" {
			return fmt.Errorf("pi driver state last_completed_turn_id is required")
		}
		return nil
	default:
		return fmt.Errorf("pi driver state_kind = %q", got)
	}
}
