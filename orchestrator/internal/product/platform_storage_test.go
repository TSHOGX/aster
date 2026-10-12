package product

import (
	"math"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/content"
)

func TestPlatformStorageCountsPublicationObjectsAndRetainsUncertainIntent(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	entries := []content.Entry{{Path: "seed", Size: 1, Digest: "sha256:" + strings.Repeat("a", 64)}}
	input := content.Publication{StoreID: "engine", Purpose: "assets", RevisionDigest: content.Digest(entries), Capacity: 8 << 20, Entries: entries}
	check := func(allocated, reserved, published int64) PlatformStorage {
		t.Helper()
		usage, err := s.OperatorStorageUsage(ctx)
		must(t, err)
		if usage.Publications != (ResourceUsage{Used: allocated, Reserved: reserved}) || usage.PublishedBytes != published {
			t.Fatal(usage)
		}
		return usage
	}
	row, err := s.ClaimContentPublication(ctx, input)
	must(t, err)
	check(0, 16<<20, 0)
	_, err = s.ClaimContentPublication(ctx, input)
	must(t, err)
	check(0, 16<<20, 0)
	row, err = s.RecordContentStaging(ctx, row, "staging")
	must(t, err)
	check(8<<20, 8<<20, 0)
	row, err = s.StartContentPopulation(ctx, row)
	must(t, err)
	row, err = s.FinishContentPopulation(ctx, row)
	must(t, err)
	row, err = s.RecordContentDerivation(ctx, row, "published")
	must(t, err)
	check(16<<20, 0, 0)
	row, err = s.CommitContentPublication(ctx, row, "published", input.RevisionDigest)
	must(t, err)
	check(16<<20, 0, 8<<20)
	// One publication can be selected by many registry identities and mounts.
	// References add no physical capacity: only the two source objects count.
	for _, id := range []string{"examples", "reference"} {
		err = s.RegisterBuiltinVolume(ctx, BuiltinVolume{Reference: ArtifactReference{ID: id, Version: "v1", Digest: input.RevisionDigest}, StoreID: "engine", PublicationPurpose: "assets", MinimumBytes: 1, Roles: []string{"data"}, Entries: entries})
		must(t, err)
	}
	check(16<<20, 0, 8<<20)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	usage := check(16<<20, 0, 8<<20)
	if usage.Owned != (ResourceUsage{Reserved: 96 << 20}) || usage.Total != (ResourceUsage{Used: 16 << 20, Reserved: 96 << 20}) {
		t.Fatal(usage)
	}
	drive(t, l, scope.ProjectID)
	usage = check(16<<20, 0, 8<<20)
	if usage.Owned != (ResourceUsage{Used: 96 << 20}) || usage.Total != (ResourceUsage{Used: 112 << 20}) {
		t.Fatal(usage)
	}
	input.Purpose = "config"
	failed, err := s.ClaimContentPublication(ctx, input)
	must(t, err)
	failed, err = s.RecordContentStaging(ctx, failed, "failed-staging")
	must(t, err)
	must(t, s.FailContentPublication(ctx, failed, "verification refused"))
	check(24<<20, 8<<20, 8<<20)
	project, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, project.Revision))
	drive(t, l, scope.ProjectID)
	usage = check(24<<20, 8<<20, 8<<20)
	if usage.Owned != (ResourceUsage{}) || usage.Total != usage.Publications {
		t.Fatal(usage)
	}
	diagnostics, err := s.OperatorDiagnostics(ctx)
	must(t, err)
	if diagnostics.Storage != usage {
		t.Fatal(diagnostics.Storage, usage)
	}
	input.Capacity = math.MaxInt64
	_, err = s.ClaimContentPublication(ctx, input)
	wantError(t, err, ErrInvalid)
}

func TestPublicationReservationOverflowRollsBackNewClaim(t *testing.T) {
	s := testStore(t)
	entries := []content.Entry{{Path: "seed", Directory: true}}
	input := content.Publication{StoreID: "engine", Purpose: "assets", RevisionDigest: content.Digest(entries), Capacity: math.MaxInt64 / 2, Entries: entries}
	_, err := s.ClaimContentPublication(ctx, input)
	must(t, err)
	input.Purpose = "config"
	input.Capacity = 1
	_, err = s.ClaimContentPublication(ctx, input)
	wantError(t, err, ErrInvalid)
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM content_publications`).Scan(&count))
	if count != 1 {
		t.Fatal("overflowing reservation committed", count)
	}
	usage, err := s.OperatorStorageUsage(ctx)
	must(t, err)
	if usage.Total.Reserved != math.MaxInt64-1 {
		t.Fatal(usage)
	}
}
