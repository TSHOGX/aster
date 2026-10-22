package authoring

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"
)

func uploadBranch(t *testing.T, s *Store, library string) Branch {
	t.Helper()
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	var id string
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM branches WHERE library_id=? AND name='Uploads'", library).Scan(&id); err == nil {
		b, err := s.branch(ctx, library, id)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	main, err := s.MainCommit(ctx, testActor, library)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, library, CreateBranchInput{randomID(), "Uploads", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func upload(t *testing.T, s *Store, lib, path string, revision int64, data []byte) Upload {
	t.Helper()
	branch := uploadBranch(t, s, lib)
	u, err := s.StartUpload(context.Background(), testActor, lib, UploadInput{BranchID: branch.ID, RequestID: randomID(), ExpectedRevision: revision, Path: path, Size: strconv.Itoa(len(data)), Digest: digestBytes(data)})
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func chunk(t *testing.T, s *Store, id string, offset int64, data []byte) Upload {
	t.Helper()
	u, err := s.Chunk(context.Background(), testActor, id, offset, data, digestBytes(data))
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func TestResumableUploadChunksFreezeGateAndReceipt(t *testing.T) {
	s, c := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	data := []byte("abcdef")
	u := upload(t, s, l.ID, "binary", 1, data)
	branch := uploadBranch(t, s, l.ID)
	c.Authority = s.config.Authority
	if _, err := s.Checkpoint(ctx, testActor, l.ID, branch.ID, CheckpointInput{"blocked", branch.Head, 1, "blocked"}); !errors.Is(err, ErrBusy) {
		t.Fatal("unfinished upload not blocking", err)
	}
	ack := chunk(t, s, u.ID, 0, data[:3])
	if ack.Offset != "3" {
		t.Fatal(ack)
	}
	if duplicate := chunk(t, s, u.ID, 0, data[:3]); duplicate.Offset != "3" {
		t.Fatal(duplicate)
	}
	if _, err := s.Chunk(ctx, testActor, u.ID, 0, []byte("xyz"), digestBytes([]byte("xyz"))); !errors.Is(err, ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
	if _, err := s.Chunk(ctx, Actor{Issuer: testActor.Issuer, Subject: "other"}, u.ID, 3, data[3:], digestBytes(data[3:])); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign upload write", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.uploadPath(u.ID), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte("unacknowledged"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err = Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(s.uploadPath(u.ID))
	if err != nil || info.Size() != 3 {
		t.Fatal("upload tail not reconciled", err)
	}
	if current, err := s.Upload(ctx, u.ID); err != nil || current.Offset != "3" {
		t.Fatal(current, err)
	}
	chunk(t, s, u.ID, 3, data[3:])
	input := CompleteInput{RequestID: "complete", ExpectedRevision: 1}
	result, err := s.CompleteUpload(ctx, testActor, u.ID, input)
	if err != nil || result.Revision != 2 {
		t.Fatal(result, err)
	}
	if again, err := s.CompleteUpload(ctx, testActor, u.ID, input); err != nil || again != result {
		t.Fatal("completion receipt lost", again, err)
	}
	if used, err := s.usage(ctx); err != nil || used != 6 {
		t.Fatal("upload charge not settled", used, err)
	}
	read, _, err := s.BranchContent(ctx, testActor, l.ID, branch.ID, "binary", 2)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(read)
	read.Close()
	if err != nil || string(bytes) != "abcdef" {
		t.Fatal("content mismatch", err)
	}
	if _, err := s.Checkpoint(ctx, testActor, l.ID, branch.ID, CheckpointInput{"commit", branch.Head, 2, "uploaded"}); err != nil {
		t.Fatal(err)
	}
}
func TestUploadCompletionCannotOverwriteNewTargetWithoutConfirmation(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	branch := uploadBranch(t, s, l.ID)
	if _, err := s.SaveBranchText(ctx, testActor, l.ID, branch.ID, SaveBranchTextInput{"old", 1, branch.Head, "file", "old"}); err != nil {
		t.Fatal(err)
	}
	u := upload(t, s, l.ID, "file", 2, []byte("upload"))
	chunk(t, s, u.ID, 0, []byte("upload"))
	if _, err := s.SaveBranchText(ctx, testActor, l.ID, branch.ID, SaveBranchTextInput{"new", 2, branch.Head, "file", "edited"}); err != nil {
		t.Fatal(err)
	}
	input := CompleteInput{RequestID: "complete", ExpectedRevision: 3}
	if _, err := s.CompleteUpload(ctx, testActor, u.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal("fresh revision silently authorized target overwrite", err)
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", branch.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.ConfirmTarget = attachmentTarget(tree, "file")
	if _, err = s.CompleteUpload(ctx, testActor, u.ID, input); err != nil {
		t.Fatal(err)
	}
}
func TestEmptyCorruptCanceledAndExpiredUploads(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	empty := upload(t, s, l.ID, "empty", 1, nil)
	if _, err := s.CompleteUpload(ctx, testActor, empty.ID, CompleteInput{RequestID: "empty-complete", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	corrupt := upload(t, s, l.ID, "corrupt", 2, []byte("expected"))
	chunk(t, s, corrupt.ID, 0, []byte("changed!"))
	if _, err := s.CompleteUpload(ctx, testActor, corrupt.ID, CompleteInput{RequestID: "bad-complete", ExpectedRevision: 2}); !errors.Is(err, ErrInvalid) {
		t.Fatal("digest mismatch accepted", err)
	}
	if current, err := s.Upload(ctx, corrupt.ID); err != nil || current.State != "failed" {
		t.Fatal(current, err)
	}
	canceled := upload(t, s, l.ID, "cancel", 2, []byte("data"))
	if err := s.CancelUpload(ctx, testActor, canceled.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelUpload(ctx, testActor, canceled.ID, "cancel"); err != nil {
		t.Fatal("cancel retry", err)
	}
	expired := upload(t, s, l.ID, "expire", 2, []byte("data"))
	now := s.now().Add(UploadLifetime + time.Second)
	s.now = func() time.Time { return now }
	if _, err := s.Chunk(ctx, testActor, expired.ID, 0, []byte("data"), digestBytes([]byte("data"))); !errors.Is(err, ErrConflict) {
		t.Fatal("expired upload accepted", err)
	}
	if n, err := s.ExpireUploads(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if used, err := s.usage(ctx); err != nil || used != 0 {
		t.Fatal("canceled/failed/expired quota retained", used, err)
	}
}
func TestUploadConcurrencyAndReservations(t *testing.T) {
	s, _ := testStore(t)
	s.config.QuotaBytes = 64 << 20
	ctx := context.Background()
	l := library(t, s)
	upload(t, s, l.ID, "one", 1, []byte("one"))
	upload(t, s, l.ID, "two", 1, []byte("two"))
	if _, err := s.StartUpload(ctx, testActor, l.ID, UploadInput{BranchID: uploadBranch(t, s, l.ID).ID, RequestID: "third", ExpectedRevision: 1, Path: "three", Size: "0", Digest: digestBytes(nil)}); !errors.Is(err, ErrBusy) {
		t.Fatal("author concurrency exceeded", err)
	}
	other := Actor{Issuer: testActor.Issuer, Subject: "other"}
	for _, name := range []string{"three", "four"} {
		if _, err := s.StartUpload(ctx, other, l.ID, UploadInput{BranchID: uploadBranch(t, s, l.ID).ID, RequestID: name, ExpectedRevision: 1, Path: name, Size: "0", Digest: digestBytes(nil)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartUpload(ctx, Actor{Issuer: testActor.Issuer, Subject: "third"}, l.ID, UploadInput{BranchID: uploadBranch(t, s, l.ID).ID, RequestID: "fifth", ExpectedRevision: 1, Path: "five", Size: "0", Digest: digestBytes(nil)}); !errors.Is(err, ErrBusy) {
		t.Fatal("global upload concurrency exceeded", err)
	}
	if used, err := s.usage(ctx); err != nil || used != 4*MaxChunkBytes+12 {
		t.Fatal("reservations", used, err)
	}
}
