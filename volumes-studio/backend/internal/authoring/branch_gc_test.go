package authoring

import (
	"context"
	"io"
	"testing"
)

func TestBranchDraftPinsBlobWithoutLegacyDraft(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"gc-branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"gc-save", 1, b.Head, "file", "retained"}); err != nil {
		t.Fatal(err)
	}
	var legacy int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM draft_entries").Scan(&legacy); err != nil || legacy != 0 {
		t.Fatal("fixture depends on old draft", legacy, err)
	}
	if removed, err := s.Collect(ctx); err != nil || removed != 0 {
		t.Fatal("branch blob collected", removed, err)
	}
	reader, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "file", 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(raw) != "retained" {
		t.Fatal(string(raw), err)
	}
	if _, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{"gc-remove", 2, b.Head, "remove", "file", "", false}); err != nil {
		t.Fatal(err)
	}
	if removed, err := s.Collect(ctx); err != nil || removed != 1 {
		t.Fatal("unreferenced branch blob retained", removed, err)
	}
}
