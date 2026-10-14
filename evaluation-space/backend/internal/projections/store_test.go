package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestProjectionReplayIsolationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	raw, _ := json.Marshal(config)
	projection := Projection{OperationID: "activation", Digest: evolution.Digest(raw), Configuration: config}
	if err = store.Adopt(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err = store.Adopt(ctx, projection); err != nil {
		t.Fatal(err)
	}
	changed := projection
	changed.Configuration.AutoEnabled = true
	raw, _ = json.Marshal(changed.Configuration)
	changed.Digest = evolution.Digest(raw)
	if !errors.Is(store.Adopt(ctx, changed), ErrConflict) {
		t.Fatal("revision rewritten")
	}
	if _, err = store.Read(ctx, "beta", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-group read", err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.Read(ctx, "alpha", 1)
	if err != nil || restored.Digest != projection.Digest || restored.Configuration.AutoEnabled {
		t.Fatal(restored, err)
	}
}
