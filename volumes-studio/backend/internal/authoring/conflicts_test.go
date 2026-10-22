package authoring

import (
	"context"
	"testing"
)

func TestConflictedPreparationPersistsExactInputs(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"other", "Other", main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, other.ID, SaveBranchTextInput{"other-save", 1, main.CommitID, "note", "competing"}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, other.ID, CheckpointInput{"other-commit", main.CommitID, 2, "competing"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Merge(ctx, testActor, l.ID, approvedIntegration(t, s, l, b))
	if err != nil {
		t.Fatal(err)
	}
	input := PrepareInput{"conflicting", other.ID, checkpoint.CommitID, accepted.AcceptedCommit, 1, ""}
	prepared, err := s.PrepareMerge(ctx, testActor, l.ID, input)
	if err != nil || prepared.ConflictWorkspaceID == "" || len(prepared.Conflicts) != 1 {
		t.Fatal(prepared, err)
	}
	again, err := s.PrepareMerge(ctx, testActor, l.ID, input)
	if err != nil || again.ConflictWorkspaceID != prepared.ConflictWorkspaceID {
		t.Fatal("workspace replay changed", err)
	}
	workspace, err := s.ConflictWorkspace(ctx, testActor, l.ID, prepared.ConflictWorkspaceID)
	if err != nil || workspace.Stale || workspace.SourceCommit != checkpoint.CommitID || workspace.MainCommit != accepted.AcceptedCommit {
		t.Fatal(workspace, err)
	}
	for side, want := range map[string]string{"main": "candidate", "branch": "competing"} {
		file, err := s.ConflictFile(ctx, testActor, l.ID, workspace.ID, side, "note")
		if err != nil || file.Text == nil || *file.Text != want {
			t.Fatal(side, file, err)
		}
	}
	absent, err := s.ConflictFile(ctx, testActor, l.ID, workspace.ID, "base", "note")
	if err != nil || absent.Preview != "absent" {
		t.Fatal(absent, err)
	}
	if _, err = s.ConflictFile(ctx, testActor, l.ID, workspace.ID, "main", "unrelated"); err != ErrInvalid {
		t.Fatal("unrelated conflict path accepted", err)
	}
	pinRepo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, commit := range map[string]string{"base": workspace.BaseCommit, "main": workspace.MainCommit, "source": workspace.SourceCommit} {
		head, err := pinRepo.Head(ctx, "refs/conflicts/"+workspace.ID+"/"+name)
		if err != nil || head != commit {
			t.Fatal("missing conflict pin", name, err)
		}
	}
	if err = pinRepo.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConflictFile(ctx, testActor, l.ID, workspace.ID, "branch", "note"); err != nil {
		t.Fatal("conflict content collected", err)
	}
	resolution := ConflictResolutionInput{RequestID: "resolve", ExpectedRevision: 1, Path: "note", Choice: "text", Text: "resolved"}
	saved, err := s.SaveConflictResolution(ctx, testActor, l.ID, workspace.ID, resolution)
	if err != nil || saved.Revision != 2 {
		t.Fatal(saved, err)
	}
	replay, err := s.SaveConflictResolution(ctx, testActor, l.ID, workspace.ID, resolution)
	if err != nil || replay != saved {
		t.Fatal("resolution replay changed", err)
	}
	resolution.RequestID = "stale-resolution"
	if _, err = s.SaveConflictResolution(ctx, testActor, l.ID, workspace.ID, resolution); err != ErrConflict {
		t.Fatal("stale resolution accepted", err)
	}
	workspace, err = s.ConflictWorkspace(ctx, testActor, l.ID, workspace.ID)
	if err != nil || len(workspace.Resolutions) != 1 || workspace.Resolutions[0].Text != "resolved" {
		t.Fatal(workspace, err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE libraries SET archived=1 WHERE id=?", l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteConflict(ctx, testActor, l.ID, workspace.ID, CompleteConflictInput{"archived-completion", 2}); err != ErrConflict {
		t.Fatal("archived conflict completed", err)
	}
	if _, err = s.SaveConflictResolution(ctx, testActor, l.ID, workspace.ID, ConflictResolutionInput{RequestID: "archived-save", ExpectedRevision: 2, Path: "note", Choice: "main"}); err != ErrConflict {
		t.Fatal("archived conflict edited", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE libraries SET archived=0 WHERE id=?", l.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := s.CompleteConflict(ctx, testActor, l.ID, workspace.ID, CompleteConflictInput{"complete-conflict", 2})
	if err != nil || completed.Identity == nil {
		t.Fatal(completed, err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"unapproved-resolution", completed.Digest, accepted.AcceptedCommit}); err != ErrAuthority {
		t.Fatal("resolved content bypassed review", err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"review-resolution", completed.Digest, "approve", "resolved"}); err != nil {
		t.Fatal(err)
	}
	final, err := s.Merge(ctx, testActor, l.ID, MergeInput{"accept-resolution", completed.Digest, accepted.AcceptedCommit})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := repo.ReadFile(ctx, final.AcceptedCommit, "note")
	if err != nil || string(data) != "resolved" {
		t.Fatal("wrong resolved bytes", err)
	}
	replayResult, err := s.CompleteConflict(ctx, testActor, l.ID, workspace.ID, CompleteConflictInput{"complete-conflict", 2})
	if err != nil || replayResult.Digest != completed.Digest {
		t.Fatal("completion replay changed", err)
	}
	if _, err = s.SetMergePolicy(ctx, testActor, l.ID, MergePolicyInput{"tighten-conflict", 1, 2}); err != nil {
		t.Fatal(err)
	}
	workspace, err = s.ConflictWorkspace(ctx, testActor, l.ID, prepared.ConflictWorkspaceID)
	if err != nil || !workspace.Stale {
		t.Fatal("stale conflict not identified", err)
	}
}
