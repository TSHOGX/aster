package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"volume-studio/backend/internal/history"

	"aster/contracts/evolution"
)

func approvedIntegration(t *testing.T, s *Store, l Library, b Branch) MergeInput {
	t.Helper()
	ctx := context.Background()
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"prepare-final", b.ID, b.Head, main, 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve-final", prepared.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	return MergeInput{"merge-final", prepared.Digest, main}
}
func TestMergeCrashRecovery(t *testing.T) {
	for _, point := range []string{"merge_after_intent", "merge_after_ref"} {
		t.Run(point, func(t *testing.T) {
			s, l, b, _ := reviewFixture(t)
			ctx := context.Background()
			input := approvedIntegration(t, s, l, b)
			cfg := s.config
			s.branchCrash = func(p string) error {
				if p == point {
					return ErrBusy
				}
				return nil
			}
			original, err := s.Merge(ctx, testActor, l.ID, input)
			if err == nil {
				t.Fatal("crash not injected")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			replay, err := reopened.Merge(ctx, testActor, l.ID, input)
			if err != nil || replay != original {
				t.Fatal("merge replay lost", err)
			}
			repo, err := reopened.historyRepository(ctx, l.ID)
			if err != nil {
				t.Fatal(err)
			}
			head, err := repo.Head(ctx, "refs/heads/main")
			if err != nil || head != original.AcceptedCommit {
				t.Fatal("acceptance not reconciled", err)
			}
			var count int
			if err = reopened.db.QueryRowContext(ctx, "SELECT count(*) FROM merge_operations WHERE state='settled'").Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate acceptance", count, err)
			}
		})
	}
}

type reviewRevokedAuthority struct{}

func (reviewRevokedAuthority) Authorize(_ context.Context, r evolution.AccessRequest) (evolution.AccessDecision, error) {
	return evolution.AccessDecision{Allowed: r.Action != "volume.review", PolicyRevision: 2}, nil
}
func TestMergeRechecksReviewerAuthority(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	input := approvedIntegration(t, s, l, b)
	s.config.Authority = reviewRevokedAuthority{}
	if _, err := s.Merge(context.Background(), testActor, l.ID, input); err != ErrAuthority {
		t.Fatal("revoked approval accepted", err)
	}
}

func TestMergeRejectsResultWithWrongParents(t *testing.T) {
	s, l, _, digest := reviewFixture(t)
	ctx := context.Background()
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"approve-wrong-parents", digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(ctx, testActor, l.ID, MergeInput{"wrong-parents", digest, main}); err != ErrInvalid {
		t.Fatal("non-integration commit accepted", err)
	}
}

func TestMergeRejectsUnpublishableIntegration(t *testing.T) {
	s, l, b, digest := reviewFixture(t)
	ctx := context.Background()
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	empty := history.Snapshot{Profile: l.Profile}
	commit, err := repo.Commit(ctx, empty, []string{main, b.Head}, "incomplete integration", nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT identity FROM prepared_merges WHERE digest=?", digest).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var prepared evolution.PreparedIdentity
	if err = json.Unmarshal(raw, &prepared); err != nil {
		t.Fatal(err)
	}
	prepared.Result.CommitID = commit
	prepared.Result.TreeDigest = empty.TreeDigest()
	newDigest, err := prepared.ApprovalDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO prepared_merges VALUES(?,?,?,0,?)", newDigest, l.ID, raw, s.now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO prepared_sources VALUES(?,?,?)", newDigest, b.ID, b.Head); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve-empty", newDigest, "approve", ""}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"merge-empty", newDigest, main}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unpublishable integration accepted", err)
	}
	head, err := repo.Head(ctx, "refs/heads/main")
	if err != nil || head != main {
		t.Fatal("invalid result advanced main", err)
	}
}

func TestArchivedLibraryCannotPrepareApproveOrMerge(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	if _, err := s.db.ExecContext(ctx, "UPDATE libraries SET archived=1 WHERE id=?", l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"archived-prepare", b.ID, b.Head, input.ExpectedMain, 1}); err != ErrConflict {
		t.Fatal("archived preparation accepted", err)
	}
	if _, err := s.Review(ctx, testActor, l.ID, ReviewInput{"archived-review", input.PreparedDigest, "approve", ""}); err != ErrConflict {
		t.Fatal("archived approval accepted", err)
	}
	if _, err := s.Merge(ctx, testActor, l.ID, input); err != ErrConflict {
		t.Fatal("archived merge accepted", err)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head(ctx, "refs/heads/main")
	if err != nil || head != input.ExpectedMain {
		t.Fatal("archive allowed main change", err)
	}
}
