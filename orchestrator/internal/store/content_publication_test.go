package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/content"
)

func publicationFixture(t *testing.T) (*Store, content.Publication) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "publication.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	entries := []content.Entry{
		{Path: "AGENTS.md", Size: 6, Digest: "sha256:" + strings.Repeat("a", 64)},
		{Path: "skills", Directory: true},
		{Path: "skills/one.md", Size: 3, Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	return st, content.Publication{StoreID: "store_a", Purpose: "assets", RevisionDigest: content.Digest(entries), Capacity: 8 << 20, Entries: entries}
}

func TestClaimContentPublicationIsIdempotentAndCollisionProof(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	first, err := st.ClaimContentPublication(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stage != "preparing" || first.StagingVolumeID != "" || first.PublishedVolumeID != "" {
		t.Fatalf("fresh claim: %+v", first)
	}
	replay, err := st.ClaimContentPublication(ctx, claim)
	if err != nil || replay.Stage != "preparing" || !replay.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replayed claim: %+v %v", replay, err)
	}
	// A revision digest names content. A claim whose entries disagree with the
	// stored revision must be refused, never allowed to redefine that identity.
	conflicting := claim
	conflicting.Entries = append([]content.Entry(nil), claim.Entries...)
	conflicting.Entries[0].Digest = "sha256:" + strings.Repeat("c", 64)
	if _, err := st.ClaimContentPublication(ctx, conflicting); err == nil {
		t.Fatal("entries disagreeing with the stored revision were accepted")
	}
	resized := claim
	resized.Capacity = 16 << 20
	if _, err := st.ClaimContentPublication(ctx, resized); err == nil {
		t.Fatal("a different capacity reused an existing publication")
	}
}

func TestClaimContentPublicationValidatesRequest(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	bad := []content.Publication{
		func() content.Publication { p := claim; p.StoreID = ""; return p }(),
		func() content.Publication { p := claim; p.Purpose = "knowledge"; return p }(),
		func() content.Publication { p := claim; p.RevisionDigest = "not-a-digest"; return p }(),
		func() content.Publication { p := claim; p.Capacity = 0; return p }(),
		func() content.Publication { p := claim; p.Entries = nil; return p }(),
		func() content.Publication { p := claim; p.RevisionDigest = content.Digest(nil); return p }(),
	}
	for i, p := range bad {
		if _, err := st.ClaimContentPublication(ctx, p); err == nil {
			t.Fatalf("case %d accepted an incoherent claim", i)
		}
	}
}

func TestPublicationStagesCommitOnlyVerifiedContent(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	record, err := st.ClaimContentPublication(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.FindPublishedContent(ctx, claim.StoreID, claim.Purpose, claim.RevisionDigest); err != nil || found {
		t.Fatalf("unprepared revision resolved to a volume: %v %v", found, err)
	}
	// Committing before derivation must be refused: a published pointer may only
	// name bytes that were derived and then read back.
	if _, err := st.CommitContentPublication(ctx, record, "vol_x", claim.RevisionDigest); err == nil {
		t.Fatal("commit without derivation succeeded")
	}
	staged, err := st.RecordContentStaging(ctx, record, "vol_staging")
	if err != nil {
		t.Fatal(err)
	}
	if staged.Stage != "staged" || staged.StagingVolumeID != "vol_staging" {
		t.Fatalf("staged: %+v", staged)
	}
	if _, err := st.RecordContentStaging(ctx, staged, "vol_other"); err == nil {
		t.Fatal("restaging on a different volume succeeded")
	}
	if replay, err := st.RecordContentStaging(ctx, staged, "vol_staging"); err != nil || replay.StagingVolumeID != "vol_staging" {
		t.Fatalf("idempotent staging: %+v %v", replay, err)
	}
	if _, err := st.RecordContentDerivation(ctx, staged, "vol_staging"); err == nil {
		t.Fatal("deriving onto the staging volume succeeded")
	}
	derived, err := st.RecordContentDerivation(ctx, staged, "vol_published")
	if err != nil {
		t.Fatal(err)
	}
	if derived.Stage != "derived" || derived.DerivedVolumeID != "vol_published" {
		t.Fatalf("derived: %+v", derived)
	}
	if _, err := st.RecordContentDerivation(ctx, derived, "vol_another"); err == nil {
		t.Fatal("re-deriving a different volume succeeded")
	}
	// An unverified revision still resolves to nothing a launch can mount.
	if _, found, err := st.FindPublishedContent(ctx, claim.StoreID, claim.Purpose, claim.RevisionDigest); err != nil || found {
		t.Fatalf("derived-but-unverified revision resolved: %v %v", found, err)
	}
	if _, err := st.CommitContentPublication(ctx, derived, "vol_published", "sha256:"+strings.Repeat("d", 64)); err == nil {
		t.Fatal("commit with an unmatched verified digest succeeded")
	}
	if _, err := st.CommitContentPublication(ctx, derived, "vol_elsewhere", claim.RevisionDigest); err == nil {
		t.Fatal("commit of a volume that was never derived succeeded")
	}
	committed, err := st.CommitContentPublication(ctx, derived, "vol_published", claim.RevisionDigest)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Stage != "published" || committed.PublishedVolumeID != "vol_published" {
		t.Fatalf("committed: %+v", committed)
	}
	volume, found, err := st.FindPublishedContent(ctx, claim.StoreID, claim.Purpose, claim.RevisionDigest)
	if err != nil || !found || volume != "vol_published" {
		t.Fatalf("published lookup: %s %v %v", volume, found, err)
	}
	if again, err := st.CommitContentPublication(ctx, committed, "vol_published", claim.RevisionDigest); err != nil || again.PublishedVolumeID != "vol_published" {
		t.Fatalf("idempotent commit: %+v %v", again, err)
	}
	// Published content is never rewritten, superseded in place, or failed.
	if _, err := st.CommitContentPublication(ctx, committed, "vol_replacement", claim.RevisionDigest); err == nil {
		t.Fatal("a published revision accepted a replacement volume")
	}
	if err := st.FailContentPublication(ctx, committed, "operator error"); err == nil {
		t.Fatal("published content was marked failed")
	}
	if replay, err := st.ClaimContentPublication(ctx, claim); err != nil || replay.Stage != "published" || replay.PublishedVolumeID != "vol_published" {
		t.Fatalf("claim after publication must resume, not reprepare: %+v %v", replay, err)
	}
}

func TestPublicationFailureIsTerminalAndDiagnosable(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	record, err := st.ClaimContentPublication(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := st.RecordContentStaging(ctx, record, "vol_staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FailContentPublication(ctx, staged, ""); err == nil {
		t.Fatal("failure without a reason was accepted")
	}
	if err := st.FailContentPublication(ctx, staged, "published bytes did not match the revision"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordContentDerivation(ctx, staged, "vol_published"); err == nil {
		t.Fatal("a failed publication kept advancing")
	}
	if _, found, err := st.FindPublishedContent(ctx, claim.StoreID, claim.Purpose, claim.RevisionDigest); err != nil || found {
		t.Fatalf("failed revision resolved to a volume: %v %v", found, err)
	}
	rows, err := st.ListContentPublications(ctx, claim.StoreID)
	if err != nil || len(rows) != 1 || rows[0].Stage != "failed" || rows[0].Error == "" {
		t.Fatalf("operability listing: %+v %v", rows, err)
	}
	if rows[0].StagingVolumeID != "vol_staging" {
		t.Fatalf("failed preparation lost its pinned staging volume: %+v", rows[0])
	}
}

func TestPublicationRefusesBlockedEngineIdentity(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	if _, err := st.db.ExecContext(ctx, `INSERT INTO engine_backend_blocks(store_id,reason) VALUES(?,?)`, claim.StoreID, "engine store replaced"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimContentPublication(ctx, claim); err == nil {
		t.Fatal("a blocked engine store accepted a publication claim")
	}
}

func TestDistinctRevisionsPublishIndependently(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	second := claim
	second.Entries = append([]content.Entry(nil), claim.Entries...)
	second.Entries[0].Digest = "sha256:" + strings.Repeat("e", 64)
	second.RevisionDigest = content.Digest(second.Entries)
	for i, p := range []content.Publication{claim, second} {
		record, err := st.ClaimContentPublication(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := st.RecordContentStaging(ctx, record, "vol_staging_"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		derived, err := st.RecordContentDerivation(ctx, staged, "vol_published_"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CommitContentPublication(ctx, derived, derived.DerivedVolumeID, p.RevisionDigest); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.ListContentPublications(ctx, claim.StoreID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("expected two independent revisions: %+v %v", rows, err)
	}
	first, err := st.ClaimContentPublication(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	// Publishing a newer revision must not disturb the older published pointer.
	if first.PublishedVolumeID != "vol_published_a" {
		t.Fatalf("superseded revision changed: %+v", first)
	}
}

func TestPublicationVolumesAreNotSharedAcrossRevisions(t *testing.T) {
	ctx := context.Background()
	st, claim := publicationFixture(t)
	second := claim
	second.Entries = append([]content.Entry(nil), claim.Entries...)
	second.Entries[0].Size = 7
	second.RevisionDigest = content.Digest(second.Entries)
	one, err := st.ClaimContentPublication(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordContentStaging(ctx, one, "vol_staging"); err != nil {
		t.Fatal(err)
	}
	two, err := st.ClaimContentPublication(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	// A replayed engine key observed under a second revision would otherwise let
	// that revision adopt the first's volume and its content.
	if _, err := st.RecordContentStaging(ctx, two, "vol_staging"); err == nil {
		t.Fatal("two revisions shared one staging volume")
	}
}
