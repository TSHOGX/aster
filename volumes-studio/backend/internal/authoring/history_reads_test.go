package authoring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryAndComparisonRemainPinnedAfterBranchAdvances(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	original := b.Head
	accepted, err := s.Merge(ctx, testActor, l.ID, approvedIntegration(t, s, l, b))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.History(ctx, testActor, l.ID, accepted.AcceptedCommit, "", 1)
	if err != nil || len(first.Commits) != 1 || len(first.Commits[0].Parents) != 2 || first.Next == "" {
		t.Fatal(first, err)
	}
	if first.Commits[0].Parents[0] != b.InitialBase || first.Commits[0].Parents[1] != b.Head {
		t.Fatal("merge parent order", first)
	}
	second, err := s.History(ctx, testActor, l.ID, accepted.AcceptedCommit, first.Next, 1)
	if err != nil || len(second.Commits) != 1 || second.Commits[0].ID != original {
		t.Fatal(second, err)
	}
	comparison, err := s.CompareSnapshots(ctx, testActor, l.ID, b.InitialBase, original, "")
	if err != nil || comparison.Total != 1 || comparison.Changes[0].Before != nil || comparison.Changes[0].After == nil {
		t.Fatal(comparison, err)
	}
	file, err := s.CommitFile(ctx, testActor, l.ID, original, "note")
	if err != nil || file.Text == nil || *file.Text != "candidate" {
		t.Fatal(file, err)
	}
	editDraft(t, s, b, draftEdit{RequestID: "later-edit", ExpectedRevision: 2, Action: "text", Path: "note", Text: "later"})
	if _, err = s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"later-checkpoint", b.Head, 3, "later"}); err != nil {
		t.Fatal(err)
	}
	again, err := s.History(ctx, testActor, l.ID, accepted.AcceptedCommit, first.Next, 1)
	if err != nil || !reflect.DeepEqual(second, again) {
		t.Fatal("history followed moving branch", again, err)
	}
	after, err := s.CompareSnapshots(ctx, testActor, l.ID, b.InitialBase, original, "")
	if err != nil || !reflect.DeepEqual(comparison, after) {
		t.Fatal("comparison followed moving branch", after, err)
	}
	if _, err = s.History(ctx, testActor, l.ID, original, first.Next, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor changed head", err)
	}
	if _, err = s.History(ctx, Actor{Issuer: testActor.Issuer, Subject: "other"}, l.ID, accepted.AcceptedCommit, first.Next, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor changed actor", err)
	}
	if _, err = s.History(ctx, testActor, l.ID, strings.Repeat("f", 64), "", 50); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing history", err)
	}
	if _, err = s.CompareSnapshots(ctx, testActor, l.ID, b.InitialBase, strings.Repeat("f", 64), ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing comparison", err)
	}
	s.config.Authority = &branchAuthority{false}
	if _, err = s.History(ctx, testActor, l.ID, accepted.AcceptedCommit, first.Next, 1); !errors.Is(err, ErrAuthority) {
		t.Fatal(err)
	}
	if _, err = s.CompareSnapshots(ctx, testActor, l.ID, b.InitialBase, original, ""); !errors.Is(err, ErrAuthority) {
		t.Fatal(err)
	}
	if _, err = s.CommitFile(ctx, testActor, l.ID, original, "note"); !errors.Is(err, ErrAuthority) {
		t.Fatal(err)
	}
}

func TestSnapshotComparisonPaginationAndPreviewKinds(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	binary := []byte{0, 1, 255}
	s.mu.Lock()
	digest, err := s.install(ctx, binary)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		if _, err = tx.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,?,'file',3,?)", b.ID, fmt.Sprintf("file-%03d", i), digest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,'empty','directory',0,NULL)", b.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"binary-tree", b.Head, 1, "Binary and empty directory"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CompareSnapshots(ctx, testActor, l.ID, b.Head, checkpoint.CommitID, "")
	if err != nil || first.Total != 206 || len(first.Changes) != 200 || first.Next == "" {
		t.Fatal(first, err)
	}
	next, err := s.CompareSnapshots(ctx, testActor, l.ID, b.Head, checkpoint.CommitID, first.Next)
	if err != nil || next.Total != 206 || len(next.Changes) != 6 || next.Next != "" || first.Changes[199].Path >= next.Changes[0].Path {
		t.Fatal(next, err)
	}
	if _, err = s.CompareSnapshots(ctx, testActor, l.ID, checkpoint.CommitID, b.Head, first.Next); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor reversed comparison", err)
	}
	for path, kind := range map[string]string{"empty": "directory", "file-000": "binary"} {
		file, err := s.CommitFile(ctx, testActor, l.ID, checkpoint.CommitID, path)
		if err != nil || file.Preview != kind || file.Text != nil {
			t.Fatal(file, err)
		}
	}
	if _, err = s.CommitFile(ctx, testActor, l.ID, checkpoint.CommitID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, invalid := range []string{"main", "--all", "HEAD~1"} {
		if _, err = s.History(ctx, testActor, l.ID, invalid, "", 50); !errors.Is(err, ErrInvalid) {
			t.Fatal("mutable revision accepted", err)
		}
	}
}

func TestCommitDownloadRematerializesAndPinsLargeImmutableContent(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	b := newDraftBranch(t, s, l.ID)
	data := bytes.Repeat([]byte("x"), MaxTextBytes+1)
	s.mu.Lock()
	digest, err := s.install(ctx, data)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,'large','file',?,?)", b.ID, len(data), digest); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"large", b.Head, 1, "large file"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{"remove", 1, checkpoint.CommitID, "remove", "large", "", false}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Collect(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	preview, err := s.CommitFile(ctx, testActor, l.ID, checkpoint.CommitID, "large")
	if err != nil || preview.Preview != "too_large" || preview.Text != nil {
		t.Fatal(preview, err)
	}
	reader, entry, err := s.CommitContent(ctx, testActor, l.ID, checkpoint.CommitID, "large")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if n, err := s.Collect(ctx); err != nil || n != 0 {
		t.Fatal("active immutable reader collected", n, err)
	}
	actual, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(actual, data) || entry.Digest != digest {
		t.Fatal("immutable download changed", err)
	}
	reader.Close()
	if n, err := s.Collect(ctx); err != nil || n != 1 {
		t.Fatal("transient materialization retained", n, err)
	}
}
