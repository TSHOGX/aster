// Package driverstate validates native driver sources outside persistence.
package driverstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/local"
)

func ValidateLocal(ctx context.Context, driverID string, payload []byte, home, completedTurn string) (string, error) {
	if driverID == string(agents.Shell) {
		return "", nil
	}
	if len(payload) == 0 {
		return "", fmt.Errorf("pi runtime launch requires driver state payload")
	}
	canonical, _, err := store.CanonicalDriverStatePayload(payload, driverID)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(canonical, payload) {
		return "", fmt.Errorf("driver state payload is not canonical")
	}
	if strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("pi agent_home host path is required")
	}
	var state struct {
		Kind      string `json:"state_kind"`
		Relative  string `json:"selected_session_relpath"`
		Completed string `json:"last_completed_turn_id"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return "", err
	}
	if state.Kind == "pi_uninitialized" {
		if completedTurn != "" {
			return "", fmt.Errorf("pi completed turn must advance to pi_session state")
		}
		return "", nil
	}
	if completedTurn != "" && state.Completed != completedTurn {
		return "", fmt.Errorf("pi driver state last_completed_turn_id mismatch")
	}
	if _, err := workspace.CleanPath(state.Relative); err != nil {
		return "", err
	}
	reader, _, err := workspace.Open(ctx, local.Directory{Path: home}, ".pi/agent/sessions/"+state.Relative)
	if err != nil {
		return "", fmt.Errorf("pi selected session host file missing or symlink path: %w", err)
	}
	defer reader.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
