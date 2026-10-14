package authoring

import (
	"context"
	"encoding/json"
	"testing"

	"aster/contracts/evolution"
)

func reviewFixture(t *testing.T) (*Store, Library, Branch, string) {
	t.Helper()
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
	_, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"save", 1, base, "note", "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"checkpoint", base, 2, "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	b.Head = checkpoint.CommitID
	b.DraftRevision = 2
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	mainTree, err := repo.Snapshot(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := repo.Snapshot(ctx, b.Head)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(commit, digest string) evolution.ContentRef {
		return evolution.ContentRef{LibraryID: l.ID, CommitID: commit, Profile: l.Profile, TreeDigest: digest}
	}
	policy := evolution.Digest([]byte("expert-policy"))
	identity := evolution.PreparedIdentity{LibraryID: l.ID, Main: ref(base, mainTree.TreeDigest()), Sources: []evolution.ContentRef{ref(b.Head, candidate.TreeDigest())}, Result: ref(b.Head, candidate.TreeDigest()), PolicyRevision: 1, PolicyDigest: policy, Checks: []evolution.CheckRequirement{}}
	digest, err := identity.ApprovalDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(identity)
	if _, err = s.db.ExecContext(ctx, "UPDATE library_merge_policies SET digest=? WHERE library_id=?", policy, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO prepared_merges VALUES(?,?,?,0,?)", digest, l.ID, raw, s.now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO prepared_sources VALUES(?,?,?)", digest, b.ID, b.Head); err != nil {
		t.Fatal(err)
	}
	return s, l, b, digest
}
func TestReviewExactIdentityAndReplay(t *testing.T) {
	s, l, _, digest := reviewFixture(t)
	input := ReviewInput{"review", digest, "approve", "checked"}
	first, err := s.Review(context.Background(), testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Review(context.Background(), testActor, l.ID, input)
	if err != nil || first.ID != again.ID {
		t.Fatal("review replay changed", err)
	}
}
func TestReviewRejectsStaleInputs(t *testing.T) {
	for _, change := range []string{"source", "policy", "conflict"} {
		t.Run(change, func(t *testing.T) {
			s, l, b, digest := reviewFixture(t)
			ctx := context.Background()
			switch change {
			case "source":
				_, err := s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"later", 2, b.Head, "note", "later"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"later-commit", b.Head, 3, "later"}); err != nil {
					t.Fatal(err)
				}
			case "policy":
				if _, err := s.db.ExecContext(ctx, "UPDATE library_merge_policies SET revision=2 WHERE library_id=?", l.ID); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				if _, err := s.db.ExecContext(ctx, "UPDATE prepared_merges SET conflicts=1 WHERE digest=?", digest); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"review", digest, "approve", ""}); err != ErrConflict {
				t.Fatal("stale preparation approved", err)
			}
		})
	}
}

func TestPrepareIntegrationAndReview(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	input := PrepareInput{RequestID: "prepare", BranchID: b.ID, ExpectedHead: b.Head, ExpectedMain: main, ExpectedPolicyRevision: 1}
	prepared, err := s.PrepareMerge(ctx, testActor, l.ID, input)
	if err != nil || prepared.Identity == nil || len(prepared.Conflicts) > 0 {
		t.Fatal(prepared, err)
	}
	if prepared.Identity.Result.CommitID == b.Head {
		t.Fatal("integration did not create commit")
	}
	data, err := repo.ReadFile(ctx, prepared.Identity.Result.CommitID, "note")
	if err != nil || string(data) != "candidate" {
		t.Fatal(string(data), err)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"approve-prepared", prepared.Digest, "approve", "Reviewed integration"}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repo.Head(ctx, "refs/heads/main")
	if err != nil || unchanged != main {
		t.Fatal("preparation advanced main")
	}
	replay, err := s.PrepareMerge(ctx, testActor, l.ID, input)
	if err != nil || replay.Digest != prepared.Digest {
		t.Fatal("preparation replay changed", err)
	}
}

func TestMergeRequiresApprovalAndPreservesReplay(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"prepare-merge", b.ID, b.Head, main, 1})
	if err != nil {
		t.Fatal(err)
	}
	input := MergeInput{"merge", prepared.Digest, main}
	if _, err := s.Merge(ctx, testActor, l.ID, input); err != ErrAuthority {
		t.Fatal("unapproved merge accepted", err)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"review-merge", prepared.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Merge(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head(ctx, "refs/heads/main")
	if err != nil || head != accepted.AcceptedCommit {
		t.Fatal("main not advanced", err)
	}
	replay, err := s.Merge(ctx, testActor, l.ID, input)
	if err != nil || replay != accepted {
		t.Fatal("merge replay changed", err)
	}
}

func TestIntegrationReadPreservesIdentityAndStaleness(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	view, err := s.Integration(ctx, testActor, l.ID, input.PreparedDigest)
	if err != nil || view.Readiness != "ready_for_review" || len(view.Reviews) != 1 {
		t.Fatal(view, err)
	}
	if _, err = s.SetMergePolicy(ctx, testActor, l.ID, MergePolicyInput{"tighten", 1, 2}); err != nil {
		t.Fatal(err)
	}
	stale, err := s.Integration(ctx, testActor, l.ID, input.PreparedDigest)
	if err != nil || stale.Readiness != "stale_or_conflicted" || stale.Identity.Result != view.Identity.Result {
		t.Fatal("stale identity changed", stale, err)
	}
	s.config.Authority = &branchAuthority{false}
	if _, err = s.Integration(ctx, testActor, l.ID, input.PreparedDigest); err != ErrAuthority {
		t.Fatal("revoked integration visible", err)
	}
}

func TestAcceptedIntegrationRemainsHistorical(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	accepted, err := s.Merge(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Integration(ctx, testActor, l.ID, input.PreparedDigest)
	if err != nil || view.Acceptance != "accepted" || view.Readiness != "historical" || view.AcceptedCommit != accepted.AcceptedCommit || view.MergeOperationID != accepted.OperationID {
		t.Fatal(view, err)
	}
	if _, err = s.SetMergePolicy(ctx, testActor, l.ID, MergePolicyInput{"later-policy", 1, 2}); err != nil {
		t.Fatal(err)
	}
	view, err = s.Integration(ctx, testActor, l.ID, input.PreparedDigest)
	if err != nil || view.Acceptance != "accepted" || view.Identity.Result.CommitID != accepted.AcceptedCommit {
		t.Fatal("history rewritten by policy", view, err)
	}
}

func TestIntegrationComparisonUsesFrozenResult(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	changes, err := s.IntegrationChanges(ctx, testActor, l.ID, input.PreparedDigest, "")
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].Path != "note" {
		t.Fatal(changes, err)
	}
	file, err := s.IntegrationFile(ctx, testActor, l.ID, input.PreparedDigest, "result", "note")
	if err != nil || file.Text == nil || *file.Text != "candidate" {
		t.Fatal(file, err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, b.ID, SaveBranchTextInput{"later-save", 2, b.Head, "note", "uncommitted"}); err != nil {
		t.Fatal(err)
	}
	file, err = s.IntegrationFile(ctx, testActor, l.ID, input.PreparedDigest, "result", "note")
	if err != nil || file.Text == nil || *file.Text != "candidate" {
		t.Fatal("review read mutable draft", file, err)
	}
}

func TestReviewHistoryRetainedAndLatestVerdictGatesMerge(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"withdraw", input.PreparedDigest, "changes_requested", "Needs correction"}); err != nil {
		t.Fatal(err)
	}
	view, err := s.Integration(ctx, testActor, l.ID, input.PreparedDigest)
	if err != nil || len(view.Reviews) != 2 || view.Reviews[0].Verdict != "approve" || view.Reviews[1].Verdict != "changes_requested" {
		t.Fatal("history overwritten", view, err)
	}
	if _, err := s.Merge(ctx, testActor, l.ID, input); err != ErrAuthority {
		t.Fatal("superseded approval counted", err)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"approve-again", input.PreparedDigest, "approve", "Rechecked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(ctx, testActor, l.ID, input); err != nil {
		t.Fatal(err)
	}
}
