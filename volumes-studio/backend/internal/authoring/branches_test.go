package authoring

import (
	"context"
	"io"
	"testing"

	"aster/contracts/evolution"
)

type branchAuthority struct{ allowed bool }

func (a *branchAuthority) Authorize(context.Context, evolution.AccessRequest) (evolution.AccessDecision, error) {
	return evolution.AccessDecision{Allowed: a.allowed, PolicyRevision: 1}, nil
}
func TestBranchIsolationAndCurrentAuthority(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	authority := &branchAuthority{true}
	s.config.Authority = authority
	base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch-one", "First", base})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch-two", "Second", base})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.Head != base || second.Head != base {
		t.Fatal("branch identity lost")
	}
	again, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch-one", "First", base})
	if err != nil || again.ID != first.ID {
		t.Fatal("branch receipt changed", err)
	}
	if _, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"forbidden-main", "main", base}); err == nil {
		t.Fatal("mutable main accepted")
	}
	authority.allowed = false
	if _, err := s.Branch(ctx, testActor, l.ID, first.ID); err != ErrAuthority {
		t.Fatal("revoked reader accepted", err)
	}
	if _, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch-one", "First", base}); err != ErrAuthority {
		t.Fatal("replay bypassed authority", err)
	}
}

func TestBranchCreationCrashRecovery(t *testing.T) {
	for _, point := range []string{"after_intent", "after_ref"} {
		t.Run(point, func(t *testing.T) {
			s, cfg := testStore(t)
			ctx := context.Background()
			l := library(t, s)
			authority := &branchAuthority{true}
			s.config.Authority = authority
			cfg.Authority = authority
			base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
			if err != nil {
				t.Fatal(err)
			}
			s.branchCrash = func(p string) error {
				if p == point {
					return ErrBusy
				}
				return nil
			}
			input := CreateBranchInput{"create-branch", "Recovered", base}
			original, err := s.CreateBranch(ctx, testActor, l.ID, input)
			if err == nil {
				t.Fatal("crash not injected")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			replay, err := reopened.CreateBranch(ctx, testActor, l.ID, input)
			if err != nil || replay.ID != original.ID {
				t.Fatalf("lost operation identity: %+v %v", replay, err)
			}
			current, err := reopened.Branch(ctx, testActor, l.ID, replay.ID)
			if err != nil || current.Head != base {
				t.Fatalf("branch not settled: %+v %v", current, err)
			}
			var count int
			if err := reopened.db.QueryRowContext(ctx, "SELECT count(*) FROM branches WHERE library_id=?", l.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate branch", count, err)
			}
		})
	}
}

func TestBranchSavesDoNotAdvanceHeadsOrOtherDrafts(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"human", "Human", base})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"other", "Other", base})
	if err != nil {
		t.Fatal(err)
	}
	save := SaveBranchTextInput{RequestID: "save-human", ExpectedRevision: 1, ExpectedHead: base, Path: "note", Text: "human draft"}
	result, err := s.SaveBranchText(ctx, testActor, l.ID, h.ID, save)
	if err != nil || result.Revision != 2 {
		t.Fatal(result, err)
	}
	human, err := s.Branch(ctx, testActor, l.ID, h.ID)
	if err != nil || human.Head != base || human.DraftRevision != 2 {
		t.Fatal("save changed commit", human, err)
	}
	other, err := s.Branch(ctx, testActor, l.ID, j.ID)
	if err != nil || other.DraftRevision != 1 {
		t.Fatal("sibling draft changed", other, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM branch_entries WHERE branch_id=?", j.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("sibling inherited edits", count, err)
	}
	save.RequestID = "stale"
	save.Text = "overwrite"
	if _, err := s.SaveBranchText(ctx, testActor, l.ID, h.ID, save); err != ErrConflict {
		t.Fatal("stale save accepted", err)
	}
	save.RequestID = "save-human"
	save.Text = "human draft"
	if _, err := s.SaveBranchText(ctx, testActor, l.ID, j.ID, save); err != ErrConflict {
		t.Fatal("receipt crossed branch", err)
	}
}

func TestCheckpointFreezesSavedDraft(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", base})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"save", 1, base, "note", "first"})
	if err != nil {
		t.Fatal(err)
	}
	input := CheckpointInput{"commit", base, 2, "First checkpoint"}
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.CommitID == base {
		t.Fatal("checkpoint did not advance")
	}
	_, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"later", 2, checkpoint.CommitID, "note", "later"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := repo.ReadFile(ctx, checkpoint.CommitID, "note")
	if err != nil || string(data) != "first" {
		t.Fatal("saved buffer changed checkpoint", err)
	}
	replay, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, input)
	if err != nil || replay.CommitID != checkpoint.CommitID {
		t.Fatal("checkpoint receipt changed", err)
	}
	head, err := repo.Head(ctx, "refs/heads/main")
	if err != nil || head != base {
		t.Fatal("checkpoint advanced main", err)
	}
	input.RequestID = "stale-commit"
	if _, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, input); err != ErrConflict {
		t.Fatal("stale checkpoint accepted", err)
	}
}

func TestCheckpointCrashRecovery(t *testing.T) {
	for _, point := range []string{"checkpoint_after_intent", "checkpoint_after_ref"} {
		t.Run(point, func(t *testing.T) {
			s, cfg := testStore(t)
			ctx := context.Background()
			l := library(t, s)
			authority := &branchAuthority{true}
			s.config.Authority = authority
			cfg.Authority = authority
			base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", base})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"save", 1, base, "note", "saved"})
			if err != nil {
				t.Fatal(err)
			}
			s.branchCrash = func(p string) error {
				if p == point {
					return ErrBusy
				}
				return nil
			}
			input := CheckpointInput{"commit", base, 2, "Checkpoint"}
			original, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, input)
			if err == nil {
				t.Fatal("crash not injected")
			}
			s.Close()
			reopened, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			replay, err := reopened.Checkpoint(ctx, testActor, l.ID, b.ID, input)
			if err != nil || replay.CommitID != original.CommitID {
				t.Fatal("checkpoint replay changed", err)
			}
			current, err := reopened.Branch(ctx, testActor, l.ID, b.ID)
			if err != nil || current.Head != original.CommitID {
				t.Fatal("head not recovered", err)
			}
		})
	}
}

func TestBranchStructuralEdits(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{"mkdir", 1, base, "mkdir", "folder", "", false}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"text", 2, base, "folder/file", "content"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{"move", 3, base, "move", "folder", "renamed", false})
	if err != nil || result.Affected != 2 {
		t.Fatal(result, err)
	}
	if _, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{"remove-denied", 4, base, "remove", "renamed", "", false}); err != ErrConflict {
		t.Fatal("recursive deletion did not require intent", err)
	}
	if _, err = s.MutateBranchStructure(ctx, testActor, l.ID, b.ID, BranchStructureInput{"remove", 4, base, "remove", "renamed", "", true}); err != nil {
		t.Fatal(err)
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", b.ID)
	if err != nil || len(tree) != 0 {
		t.Fatal(tree, err)
	}
}

func TestBranchReadIdentities(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	base, err := s.bootstrapHistory(ctx, l.ID, l.Profile)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"save", 1, base, "note", "draft"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "", "", 2)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	pageChanges, err := s.BranchDiff(ctx, testActor, l.ID, b.ID, base, "", 2)
	if err != nil || len(pageChanges.Changes) != 1 || pageChanges.Changes[0].Kind != "added" {
		t.Fatal(pageChanges, err)
	}
	f, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "note", 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(raw) != "draft" {
		t.Fatal(string(raw), err)
	}
	if _, _, err := s.BranchContent(ctx, testActor, l.ID, b.ID, "note", 1); err != ErrConflict {
		t.Fatal("stale content read", err)
	}
	if _, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "", "note", 0); err != ErrConflict {
		t.Fatal("unbound cursor accepted", err)
	}
}

func TestBranchCatalogScope(t *testing.T) {
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
	page, err := s.Branches(ctx, testActor, l.ID, "")
	if err != nil || len(page.Branches) != 1 || page.Branches[0].ID != branch.ID || page.Branches[0].Head != main.CommitID {
		t.Fatal(page, err)
	}
	page, err = s.Branches(ctx, testActor, l.ID, branch.ID)
	if err != nil || len(page.Branches) != 0 {
		t.Fatal(page, err)
	}
	authority.allowed = false
	if _, err = s.Branches(ctx, testActor, l.ID, ""); err != ErrAuthority {
		t.Fatal("revoked catalog visible", err)
	}
}

func TestBranchEntrySnapshotIdentity(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.BranchEntry(ctx, testActor, l.ID, b.ID, "file", 1)
	if err != nil || missing.Entry != nil || missing.Head != main.CommitID || missing.BranchID != b.ID {
		t.Fatal(missing, err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"save", 1, main.CommitID, "file", "content"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BranchEntry(ctx, testActor, l.ID, b.ID, "file", 1); err != ErrConflict {
		t.Fatal("stale entry accepted", err)
	}
	current, err := s.BranchEntry(ctx, testActor, l.ID, b.ID, "file", 0)
	if err != nil || current.Entry == nil || current.Revision != 2 {
		t.Fatal(current, err)
	}
}

func TestBranchSearchRevision(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"branch", "Work", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"save", 1, main.CommitID, "Notes.md", "content"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchBranch(ctx, testActor, l.ID, b.ID, "notes", "", 2)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	if _, err = s.SearchBranch(ctx, testActor, l.ID, b.ID, "notes", "", 1); err != ErrConflict {
		t.Fatal("stale search accepted", err)
	}
}
