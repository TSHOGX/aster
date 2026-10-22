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
	u := upload(t, s, l.ID, "unfinished", 1, data)
	chunk(t, s, u.ID, 0, data[:3])
	target := filepath.Join(t.TempDir(), "backup")
	if err := s.Backup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "backup.json")); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, Config{Authority: s.config.Authority, Directory: target, PublisherID: s.config.PublisherID, QuotaBytes: s.config.QuotaBytes})
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
	if _, err = restored.CompleteUpload(ctx, testActor, u.ID, CompleteInput{RequestID: "after-restore", ExpectedRevision: 1}); err != nil {
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
	if restored, err := Open(ctx, Config{Authority: s.config.Authority, Directory: target, PublisherID: s.config.PublisherID, QuotaBytes: s.config.QuotaBytes}); err == nil {
		restored.Close()
		t.Fatal("incomplete backup opened")
	}
	if err := s.Backup(ctx, target); err == nil {
		t.Fatal("existing incomplete directory overwritten")
	}
}

func TestBackupRestoresReviewedHistoryAndIndependentDraft(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	accepted, err := s.Merge(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.Release(ctx, testActor, l.ID, ReleaseInput{"backup-release", accepted.AcceptedCommit, "reviewed", ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"draft-after-release", 2, b.Head, "note", "uncommitted draft"}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "reviewed-backup")
	if err = s.Backup(ctx, target); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, Config{Directory: target, PublisherID: s.config.PublisherID, QuotaBytes: s.config.QuotaBytes, Authority: s.config.Authority})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	main, err := restored.MainCommit(ctx, testActor, l.ID)
	if err != nil || main.CommitID != accepted.AcceptedCommit {
		t.Fatal("accepted head lost", main, err)
	}
	version, err := restored.Version(ctx, release.ID)
	if err != nil || version.AcceptedCommit != accepted.AcceptedCommit || version.State != "published" {
		t.Fatal("release lineage lost", version, err)
	}
	view, err := restored.Integration(ctx, testActor, l.ID, input.PreparedDigest)
	if err != nil || view.Acceptance != "accepted" || len(view.Reviews) != 1 {
		t.Fatal("review history lost", view, err)
	}
	draft, _, err := restored.BranchContent(ctx, testActor, l.ID, b.ID, "note", 3)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(draft)
	draft.Close()
	if err != nil || string(raw) != "uncommitted draft" {
		t.Fatal("draft lost", err)
	}
	published, _, err := restored.Content(ctx, l.ID, "note", release.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = io.ReadAll(published)
	published.Close()
	if err != nil || string(raw) != "candidate" {
		t.Fatal("draft replaced release", err)
	}
	replay, err := restored.Merge(ctx, testActor, l.ID, input)
	if err != nil || replay != accepted {
		t.Fatal("acceptance receipt lost", err)
	}
}
