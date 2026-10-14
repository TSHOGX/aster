package authoring

import (
	"context"
	"testing"
)

func TestBranchUploadIsolation(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"first", "First", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"second", "Second", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("bytes")
	upload, err := s.StartUpload(ctx, testActor, l.ID, UploadInput{RequestID: "upload", BranchID: first.ID, ExpectedRevision: 1, Path: "file", Size: "5", Digest: digestBytes(data)})
	if err != nil {
		t.Fatal(err)
	}
	if upload.BranchID != first.ID {
		t.Fatal("upload branch missing")
	}
	if _, err := s.Checkpoint(ctx, testActor, l.ID, first.ID, CheckpointInput{"blocked", main.CommitID, 1, "blocked"}); err != ErrBusy {
		t.Fatal("unfinished upload checkpoint accepted", err)
	}
	if _, err := s.Checkpoint(ctx, testActor, l.ID, second.ID, CheckpointInput{"unrelated", main.CommitID, 1, "unrelated"}); err != nil {
		t.Fatal("sibling upload blocked checkpoint", err)
	}
	if _, err = s.Chunk(ctx, testActor, upload.ID, 0, data, digestBytes(data)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteUpload(ctx, testActor, upload.ID, CompleteInput{RequestID: "complete", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	tree, err := s.BranchTree(ctx, testActor, l.ID, first.ID, "", "", 2)
	if err != nil || len(tree.Entries) != 1 {
		t.Fatal(tree, err)
	}
	tree, err = s.BranchTree(ctx, testActor, l.ID, second.ID, "", "", 1)
	if err != nil || len(tree.Entries) != 0 {
		t.Fatal("upload crossed branches", tree, err)
	}
}

func TestUploadRevocationAndCancellationOwnership(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	authority := &branchAuthority{true}
	s.config.Authority = authority
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("bytes")
	u, err := s.StartUpload(ctx, testActor, l.ID, UploadInput{RequestID: "upload", BranchID: branch.ID, ExpectedRevision: 1, Path: "file", Size: "5", Digest: digestBytes(data)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelUpload(ctx, Actor{Issuer: testActor.Issuer, Subject: "other"}, u.ID, "cancel-other"); err != ErrNotFound {
		t.Fatal("nonowner canceled upload", err)
	}
	authority.allowed = false
	if _, err = s.Chunk(ctx, testActor, u.ID, 0, data, digestBytes(data)); err != ErrAuthority {
		t.Fatal("revoked chunk accepted", err)
	}
	authority.allowed = true
	if _, err = s.Chunk(ctx, testActor, u.ID, 0, data, digestBytes(data)); err != nil {
		t.Fatal(err)
	}
	input := CompleteInput{RequestID: "complete", ExpectedRevision: 1}
	if _, err = s.CompleteUpload(ctx, testActor, u.ID, input); err != nil {
		t.Fatal(err)
	}
	authority.allowed = false
	if _, err = s.CompleteUpload(ctx, testActor, u.ID, input); err != ErrAuthority {
		t.Fatal("receipt bypassed revocation", err)
	}
}

func TestUploadReadsRespectActorAndBranch(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	authority := &branchAuthority{true}
	s.config.Authority = authority
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.StartUpload(ctx, testActor, l.ID, UploadInput{RequestID: "upload", BranchID: b.ID, ExpectedRevision: 1, Path: "file", Size: "0", Digest: digestBytes(nil)})
	if err != nil {
		t.Fatal(err)
	}
	other := Actor{Issuer: testActor.Issuer, Subject: "other"}
	if _, err := s.ActorUpload(ctx, other, l.ID, u.ID); err != ErrNotFound {
		t.Fatal("other actor read upload", err)
	}
	list, err := s.BranchUploads(ctx, other, l.ID, b.ID)
	if err != nil || len(list) != 0 {
		t.Fatal("other actor listed upload", err)
	}
	list, err = s.BranchUploads(ctx, testActor, l.ID, b.ID)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	authority.allowed = false
	if _, err := s.ActorUpload(ctx, testActor, l.ID, u.ID); err != ErrAuthority {
		t.Fatal("revoked read allowed", err)
	}
}
