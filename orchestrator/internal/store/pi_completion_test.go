package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/execution"
)

// piCompletion builds a valid completion for the only driver there is. Pi is an
// agent with native history, so completing its turn requires advanced native
// state plus retained snapshot evidence; tests that only care about a turn
// reaching "completed" use this rather than restating that contract each time.
func piCompletion(t *testing.T, st *Store, binding execution.Binding, grant TurnGrant) CompleteTurnParams {
	t.Helper()
	ctx := context.Background()
	completion := CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: grant.TurnID, TerminalStatus: "completed"}
	payload, digest, err := CanonicalDriverStatePayload(map[string]any{
		"schema_version":           1,
		"driver_id":                string(agents.Pi),
		"state_kind":               "pi_session",
		"session_dir":              agents.PiSessionDir,
		"selected_session_file":    agents.PiSessionDir + "/native.jsonl",
		"selected_session_relpath": "native.jsonl",
		"selected_session_id":      "native-id",
		"last_completed_turn_id":   fmt.Sprint(grant.TurnID),
	}, string(agents.Pi))
	if err != nil {
		t.Fatal(err)
	}
	completion.DriverStateUpdate = &DriverStateUpdate{DriverID: string(agents.Pi), PreviousStateDigest: grant.DriverState.StateDigest, StatePayload: payload, StateDigest: digest, StateVersion: grant.DriverState.StateVersion + 1}
	input, err := st.DriverStateValidationInput(ctx, completion)
	if err != nil {
		t.Fatal(err)
	}
	input.Evidence.NativeDigest = strings.Repeat("c", 64)
	input.Evidence.NativeSnapshotPath, _ = agents.PiCompletedSnapshotPath(digest)
	input.Evidence.NativeSize = 128
	completion.DriverStateEvidence = &input.Evidence
	return completion
}
