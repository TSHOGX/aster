package authoring

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoreRetainsPublisherVersionsAndUploadCheckpoints(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l, v := released(t, s)
	data := []byte("resumable")
	u := upload(t, s, l.ID, "unfinished", 2, data)
	chunk(t, s, u.ID, 0, data[:3])
	target := filepath.Join(t.TempDir(), "backup")
	if err := s.Backup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "backup.json")); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, Config{Directory: target, PublisherID: s.config.PublisherID, QuotaBytes: s.config.QuotaBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	raw, err := restored.Descriptor(ctx, v.ID)
	if err != nil || len(raw) == 0 {
		t.Fatal("version lost", err)
	}
	f, _, err := restored.Content(ctx, l.ID, "file", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(b) != "portable bytes" {
		t.Fatal("source lost", err)
	}
	status, err := restored.Upload(ctx, u.ID)
	if err != nil || status.Offset != "3" {
		t.Fatal("checkpoint lost", status, err)
	}
	chunk(t, restored, u.ID, 3, data[3:])
	if _, err = restored.CompleteUpload(ctx, testActor, u.ID, CompleteInput{RequestID: "after-restore", ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	// Continuing one installation does not modify the independently owned copy.
	original, err := s.Upload(ctx, u.ID)
	if err != nil || original.Offset != "3" {
		t.Fatal("backup shared writable paths", original, err)
	}
}
func TestBackupRefusesOverwriteAndIncompleteRestore(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	library(t, s)
	if err := s.Backup(ctx, s.config.Directory); err == nil {
		t.Fatal("source overwritten")
	}
	if err := s.Backup(ctx, filepath.Join(s.config.Directory, "nested")); err == nil {
		t.Fatal("nested backup accepted")
	}
	target := filepath.Join(t.TempDir(), "backup")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Backup(cancelled, target); err == nil {
		t.Fatal("canceled backup succeeded")
	}
	if restored, err := Open(ctx, Config{Directory: target, PublisherID: s.config.PublisherID, QuotaBytes: s.config.QuotaBytes}); err == nil {
		restored.Close()
		t.Fatal("incomplete backup opened")
	}
	if err := s.Backup(ctx, target); err == nil {
		t.Fatal("existing incomplete directory overwritten")
	}
}
