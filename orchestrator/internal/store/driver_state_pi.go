package store

import (
	"fmt"
	"path/filepath"
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
	if sessionDir != agents.PiSessionDir && sessionDir != "/root/.pi/agent/sessions" {
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

func piAgentHomeHostPathFromContract(contract map[string]any) (string, error) {
	dataVolumes, ok := contract["data_volumes"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("pi driver state validation requires data_volumes")
	}
	agentHome, ok := dataVolumes["agent_home"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("pi driver state validation requires agent_home data volume")
	}
	if driver, _ := agentHome["driver"].(string); driver != string(agents.Pi) {
		return "", fmt.Errorf("pi agent_home data volume driver = %q", driver)
	}
	if key, _ := agentHome["driver_home_key"].(string); key != string(agents.Pi) {
		return "", fmt.Errorf("pi agent_home data volume key = %q", key)
	}
	if destination, _ := agentHome["sandbox_destination"].(string); destination != "/agent-home" {
		return "", fmt.Errorf("pi agent_home sandbox destination = %q", destination)
	}
	hostPath := strings.TrimSpace(stringValue(agentHome["host_path"]))
	if hostPath == "" || !filepath.IsAbs(hostPath) {
		return "", fmt.Errorf("pi agent_home host_path is required")
	}
	return hostPath, nil
}
