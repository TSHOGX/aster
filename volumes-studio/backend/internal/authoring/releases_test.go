package authoring

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestReleaseRequiresAcceptedCommit(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	if _, err := s.Release(ctx, testActor, l.ID, ReleaseInput{"unmerged", b.Head, "bad", ""}); err != ErrConflict {
		t.Fatal("unmerged branch released", err)
	}
	input := approvedIntegration(t, s, l, b)
	accepted, err := s.Merge(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	release := ReleaseInput{"release", accepted.AcceptedCommit, "v-reviewed", ""}
	version, err := s.Release(ctx, testActor, l.ID, release)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Release(ctx, testActor, l.ID, release)
	if err != nil || again.ID != version.ID {
		t.Fatal("release replay changed", err)
	}
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := s.Version(ctx, version.ID)
	if err != nil || v.State != "published" {
		t.Fatal(v, err)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.Snapshot(ctx, accepted.AcceptedCommit)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.Descriptor(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil || descriptor.TreeDigest != snapshot.TreeDigest() {
		t.Fatal("release differs from acceptance", err)
	}
}

type publishRevokedAuthority struct{}

func (publishRevokedAuthority) Authorize(_ context.Context, r evolution.AccessRequest) (evolution.AccessDecision, error) {
	return evolution.AccessDecision{Allowed: r.Action != "volume.publish", PolicyRevision: 2}, nil
}
func TestReleaseWorkerRechecksScopedPublishGrant(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	input := approvedIntegration(t, s, l, b)
	accepted, err := s.Merge(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	version, err := s.Release(ctx, testActor, l.ID, ReleaseInput{"release-revoked", accepted.AcceptedCommit, "revoked", ""})
	if err != nil {
		t.Fatal(err)
	}
	s.config.Authority = publishRevokedAuthority{}
	processed, err := s.ValidateNext(ctx)
	if !processed || !errors.Is(err, ErrAuthority) {
		t.Fatal("revoked publisher admitted", processed, err)
	}
	current, err := s.Version(ctx, version.ID)
	if err != nil || current.State == "published" || current.Error != "principal_authority_required" {
		t.Fatal(current, err)
	}
}

func TestReleaseRetentionCountsBranchDrafts(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	accepted, err := s.Merge(ctx, testActor, l.ID, approvedIntegration(t, s, l, b))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Release(ctx, testActor, l.ID, ReleaseInput{"release-retention", accepted.AcceptedCommit, "retained", ""})
	if err != nil {
		t.Fatal(err)
	}
	retention, err := s.Retention(ctx, v.ID)
	if err != nil || retention.DraftReferences != 1 {
		t.Fatal("branch reference missing", retention, err)
	}
	if _, err = s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"second-retainer", "Another", accepted.AcceptedCommit}); err != nil {
		t.Fatal(err)
	}
	retention, err = s.Retention(ctx, v.ID)
	if err != nil || retention.DraftReferences != 2 {
		t.Fatal("second branch not counted", retention, err)
	}
}

func TestReleaseLabelConflictDoesNotCreateReceipt(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	accepted, err := s.Merge(ctx, testActor, l.ID, approvedIntegration(t, s, l, b))
	if err != nil {
		t.Fatal(err)
	}
	input := ReleaseInput{"first-label", accepted.AcceptedCommit, "unique", ""}
	first, err := s.Release(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = "second-label"
	if _, err = s.Release(ctx, testActor, l.ID, input); err != ErrConflict {
		t.Fatal("duplicate label accepted", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM receipts WHERE request_id='second-label'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed admission recorded success", err)
	}
	input.RequestID = "first-label"
	replay, err := s.Release(ctx, testActor, l.ID, input)
	if err != nil || replay.ID != first.ID {
		t.Fatal("label check broke receipt replay", err)
	}
}
