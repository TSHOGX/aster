package driverstate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

type NativeSync interface {
	SyncNative(context.Context, string, string, string) (workspace.File, error)
}

// ValidateRemote uses a confined helper to inspect and synchronize the selected
// native file. It returns small durable evidence without copying full history
// through the turn-completion path; archival is an independent consumer.
func ValidateRemote(ctx context.Context, payload []byte, completedTurn, key string, files NativeSync) (workspace.File, error) {
	if files == nil || key == "" {
		return workspace.File{}, fmt.Errorf("native source and operation identity required")
	}
	canonical, _, err := store.CanonicalDriverStatePayload(payload, "pi")
	if err != nil {
		return workspace.File{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return workspace.File{}, fmt.Errorf("native state payload is not canonical")
	}
	var state struct {
		Kind      string `json:"state_kind"`
		NativeID  string `json:"selected_session_id"`
		Directory string `json:"session_dir"`
		Relative  string `json:"selected_session_relpath"`
		Completed string `json:"last_completed_turn_id"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return workspace.File{}, err
	}
	if state.Kind != "pi_session" || state.Directory != "/root/.pi/agent/sessions" || state.Completed != completedTurn {
		return workspace.File{}, fmt.Errorf("native state does not match retained Pi history and completed turn")
	}
	return files.SyncNative(ctx, key, state.Relative, state.NativeID)
}
