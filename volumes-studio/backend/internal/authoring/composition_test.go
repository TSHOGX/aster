package authoring

import (
	"context"
	"testing"
)

func composeBranch(t *testing.T, s *Store, library, base, name string, files map[string]string) Branch {
	t.Helper()
	ctx := context.Background()
	branch, err := s.CreateBranch(ctx, testActor, library, CreateBranchInput{randomID(), name, base})
	if err != nil {
		t.Fatal(err)
	}
	revision := branch.DraftRevision
	for path, text := range files {
		revision++
		if _, err = s.SaveBranchText(ctx, testActor, library, branch.ID, SaveBranchTextInput{randomID(), revision - 1, branch.Head, path, text}); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := s.Checkpoint(ctx, testActor, library, branch.ID, CheckpointInput{randomID(), branch.Head, revision, name})
	if err != nil {
		t.Fatal(err)
	}
	branch.Head = checkpoint.CommitID
	branch.DraftRevision = revision
	return branch
}

func TestComposeMergeSelectsChangesAcrossBranchesAndBindsDeferredWork(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := composeBranch(t, s, l.ID, main.CommitID, "first", map[string]string{"selected.txt": "selected", "deferred.txt": "deferred"})
	second := composeBranch(t, s, l.ID, main.CommitID, "second", map[string]string{"other.txt": "other"})
	prepared, err := s.ComposeMerge(ctx, testActor, l.ID, ComposeInput{
		RequestID:              "compose",
		ExpectedMain:           main.CommitID,
		ExpectedPolicyRevision: 1,
		Branches: []CompositionBranch{
			{BranchID: first.ID, ExpectedHead: first.Head, SelectedPaths: []string{"selected.txt"}},
			{BranchID: second.ID, ExpectedHead: second.Head},
		},
	})
	if err != nil || prepared.Identity == nil {
		t.Fatal(prepared, err)
	}
	if len(prepared.Selections) != 2 || len(prepared.Selections[0].DeferredPaths) != 1 {
		t.Fatal("selection/deferred metadata missing", prepared.Selections)
	}
	if len(prepared.Identity.Sources) != 2 || len(prepared.Identity.Selections) != 2 {
		t.Fatal("composed identity lost sources or selections", prepared.Identity)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"selected.txt": "selected", "other.txt": "other"} {
		data, readErr := repo.ReadFile(ctx, prepared.Identity.Result.CommitID, path)
		if readErr != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", path, string(data), readErr)
		}
	}
	if _, err = repo.ReadFile(ctx, prepared.Identity.Result.CommitID, "deferred.txt"); err == nil {
		t.Fatal("deferred change entered composed tree")
	}
	parents, err := repo.Parents(ctx, prepared.Identity.Result.CommitID)
	if err != nil || len(parents) != 3 || parents[1] == first.Head || parents[1] != prepared.Identity.Selections[0].CandidateCommit || parents[2] != second.Head {
		t.Fatal("composed commit parents lost", parents, err)
	}
	replayed, err := s.ComposeMerge(ctx, testActor, l.ID, ComposeInput{
		RequestID:              "compose",
		ExpectedMain:           main.CommitID,
		ExpectedPolicyRevision: 1,
		Branches: []CompositionBranch{
			{BranchID: first.ID, ExpectedHead: first.Head, SelectedPaths: []string{"selected.txt"}},
			{BranchID: second.ID, ExpectedHead: second.Head},
		},
	})
	if err != nil || replayed.Digest != prepared.Digest {
		t.Fatal("composition replay changed identity", replayed, err)
	}
}

func TestComposeMergeRejectsCrossBranchConflict(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := composeBranch(t, s, l.ID, main.CommitID, "first", map[string]string{"same.txt": "first"})
	second := composeBranch(t, s, l.ID, main.CommitID, "second", map[string]string{"same.txt": "second"})
	prepared, err := s.ComposeMerge(ctx, testActor, l.ID, ComposeInput{RequestID: "conflict", ExpectedMain: main.CommitID, ExpectedPolicyRevision: 1, Branches: []CompositionBranch{{BranchID: first.ID, ExpectedHead: first.Head}, {BranchID: second.ID, ExpectedHead: second.Head}}})
	if err != ErrConflict || len(prepared.Conflicts) == 0 {
		t.Fatal("conflicting same-Volume changes were accepted", prepared, err)
	}
}

func TestBranchHeadDiffExposesOnlyCommittedDelta(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	l := library(t, s)
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := composeBranch(t, s, l.ID, main.CommitID, "head-diff", map[string]string{"committed.txt": "yes"})
	page, err := s.BranchHeadDiff(ctx, testActor, l.ID, b.ID, b.Head, "")
	if err != nil || page.Total != 1 || len(page.Changes) != 1 || page.Changes[0].Path != "committed.txt" {
		t.Fatal(page, err)
	}
	if _, err = s.BranchHeadDiff(ctx, testActor, l.ID, b.ID, main.CommitID, ""); err != ErrConflict {
		t.Fatal("stale branch head was accepted", err)
	}
}
