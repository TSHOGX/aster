package driverstate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

type nativeSyncFixture struct {
	path, id, key string
	failure       error
}

func (f *nativeSyncFixture) SnapshotNative(_ context.Context, key, path, destination, id string) (workspace.File, error) {
	f.key = key
	f.path = path
	f.id = id
	return workspace.File{Path: destination, Size: 42, Digest: strings.Repeat("a", 64)}, f.failure
}
func TestRemoteNativeStateRequiresSelectedIdentityAndCompletedTurn(t *testing.T) {
	payload, _, err := store.CanonicalDriverStatePayload(map[string]any{"schema_version": 1, "driver_id": "pi", "state_kind": "pi_session", "session_dir": "/root/.pi/agent/sessions", "selected_session_file": "/root/.pi/agent/sessions/native.jsonl", "selected_session_relpath": "native.jsonl", "selected_session_id": "native-id", "last_completed_turn_id": "7"}, "pi")
	if err != nil {
		t.Fatal(err)
	}
	source := &nativeSyncFixture{}
	result, err := ValidateRemote(context.Background(), payload, "7", "native-barrier", source)
	if err != nil || result.Size != 42 || source.path != "native.jsonl" || source.id != "native-id" || source.key != "native-barrier" {
		t.Fatalf("native binding lost: %+v %+v %v", result, source, err)
	}
	if _, err := ValidateRemote(context.Background(), payload, "8", "other", source); err == nil {
		t.Fatal("native state advanced another turn")
	}
	source.failure = errors.New("native fsync failed")
	if _, err := ValidateRemote(context.Background(), payload, "7", "native-barrier", source); !errors.Is(err, source.failure) {
		t.Fatalf("failed barrier ignored: %v", err)
	}
}
