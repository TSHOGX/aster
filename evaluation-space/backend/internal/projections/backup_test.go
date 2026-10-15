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

func TestBackupRestoreRetainsProjectionAndRejectsTampering(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state := filepath.Join(root, "state", "projections.db")
	store, err := Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	config := Projection{OperationID: "op", Configuration: evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}}
	raw, _ := json.Marshal(config.Configuration)
	config.Digest = evolution.Digest(raw)
	if err = store.Adopt(ctx, config); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	if err = store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(backup, "backup.incomplete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete marker retained after success", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err = Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, filepath.Join(restored, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = opened.Read(ctx, "group", 1); err != nil {
		t.Fatal("restored projection missing", err)
	}
	opened.Close()
	if err = os.WriteFile(filepath.Join(backup, "projections.db"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, filepath.Join(root, "bad")); err == nil {
		t.Fatal("tampered backup accepted")
	}
}

func TestBackupRefusesStateDirectoryAndIncompleteRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state := filepath.Join(root, "state", "projections.db")
	store, err := Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Backup(ctx, filepath.Dir(state)); err == nil {
		t.Fatal("backup inside state directory accepted")
	}
	backup := filepath.Join(root, "incomplete")
	if err = os.MkdirAll(backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(backup, "backup.incomplete"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, filepath.Join(root, "restored")); err == nil {
		t.Fatal("incomplete backup accepted")
	}
}
