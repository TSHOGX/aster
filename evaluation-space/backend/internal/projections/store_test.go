package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"errors"
	"os"
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

func TestProjectionDatabasePrivateAndRejectsSymlink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "nested", "space.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("database permissions", info, err)
	}
	link := filepath.Join(root, "link.db")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(ctx, link); err == nil {
		opened.Close()
		t.Fatal("symlink database accepted")
	}
}

func TestProjectionStoreExclusiveLifetimeOwnership(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Open(ctx, path); err == nil {
		second.Close()
		t.Fatal("concurrent store owner admitted")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal("ownership not released", err)
	}
	defer second.Close()
	if err = first.Close(); err != nil {
		t.Fatal("close not idempotent", err)
	}
}
