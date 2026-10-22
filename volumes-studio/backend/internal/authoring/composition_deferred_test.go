package authoring

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"volume-studio/backend/internal/history"
)

func TestPartialCompositionKeepsDeferredWorkAvailableToOrdinaryPrepare(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	l := library(t, s)
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := composeBranch(t, s, l.ID, main.CommitID, "partial source", map[string]string{"selected.txt": "selected", "deferred.txt": "deferred"})
	partial, err := s.ComposeMerge(ctx, testActor, l.ID, ComposeInput{RequestID: "partial", ExpectedMain: main.CommitID, ExpectedPolicyRevision: 1, Branches: []CompositionBranch{{BranchID: source.ID, ExpectedHead: source.Head, SelectedPaths: []string{"selected.txt"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve-partial", partial.Digest, "approve", "Only selected.txt"}); err != nil {
		t.Fatal(err)
	}
	merged, err := s.Merge(ctx, testActor, l.ID, MergeInput{"merge-partial", partial.Digest, main.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	// A later independent human change must survive the remaining-source merge.
	human := composeBranch(t, s, l.ID, merged.AcceptedCommit, "human", map[string]string{"human.txt": "preserved human work"})
	humanPrepared, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{RequestID: "human-prepare", BranchID: human.ID, ExpectedHead: human.Head, ExpectedMain: merged.AcceptedCommit, ExpectedPolicyRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"human-approve", humanPrepared.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Merge(ctx, testActor, l.ID, MergeInput{"human-merge", humanPrepared.Digest, merged.AcceptedCommit})
	if err != nil {
		t.Fatal(err)
	}
	// Candidate and original-source evidence must survive collection and restore.
	if _, err = s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "partial-backup")
	if err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, Config{Directory: backup, PublisherID: s.config.PublisherID, QuotaBytes: s.config.QuotaBytes, Authority: s.config.Authority})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	s = restored
	retained, err := s.Integration(ctx, testActor, l.ID, partial.Digest)
	if err != nil || retained.Acceptance != "accepted" || retained.Identity.Selections[0].Head != source.Head || len(retained.Identity.Selections[0].DeferredPaths) != 1 {
		t.Fatal("partial lineage lost", retained, err)
	}
	remaining, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{RequestID: "remaining", BranchID: source.ID, ExpectedHead: source.Head, ExpectedMain: current.AcceptedCommit, ExpectedPolicyRevision: 1})
	if err != nil || remaining.Identity == nil {
		t.Fatal(remaining, err)
	}
	repo, err := s.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := repo.ReadFile(ctx, remaining.Identity.Result.CommitID, "deferred.txt")
	if err != nil || string(content) != "deferred" {
		t.Fatalf("deferred work was consumed by partial ancestry: %q, %v", content, err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"remaining-approve", remaining.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, source.ID, SaveBranchTextInput{"advance", source.DraftRevision, source.Head, "later.txt", "later source work"}); err != nil {
		t.Fatal(err)
	}
	advanced, err := s.Checkpoint(ctx, testActor, l.ID, source.ID, CheckpointInput{"advance-commit", source.Head, source.DraftRevision + 1, "Advance original source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"stale-source", remaining.Digest, current.AcceptedCommit}); err != ErrConflict {
		t.Fatal("partial ancestry bypassed stale source", err)
	}
	fresh, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{RequestID: "fresh-remaining", BranchID: source.ID, ExpectedHead: advanced.CommitID, ExpectedMain: current.AcceptedCommit, ExpectedPolicyRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"fresh-approve", fresh.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	final, err := s.Merge(ctx, testActor, l.ID, MergeInput{"fresh-merge", fresh.Digest, current.AcceptedCommit})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"selected.txt": "selected", "deferred.txt": "deferred", "human.txt": "preserved human work", "later.txt": "later source work"} {
		data, err := repo.ReadFile(ctx, final.AcceptedCommit, path)
		if err != nil || string(data) != want {
			t.Fatal(path, string(data), err)
		}
	}
}

func TestPartialCompositionStructuralDependencies(t *testing.T) {
	file := func(path string) manifest.Entry {
		return manifest.Entry{Path: path, Kind: "file", Size: "1", Digest: digestBytes([]byte("x"))}
	}
	dir := func(path string) manifest.Entry { return manifest.Entry{Path: path, Kind: "directory", Size: "0"} }
	b := Branch{ID: "source", Head: strings.Repeat("b", 64), InitialBase: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		name       string
		base, full []manifest.Entry
		roots      []string
		valid      bool
		paths      []string
		deferred   []string
	}{
		{"nested addition carries replaced parent", []manifest.Entry{file("shape")}, []manifest.Entry{dir("shape"), file("shape/child"), file("shape/sibling")}, []string{"shape/child"}, true, []string{"shape", "shape/child"}, []string{"shape/sibling"}},
		{"directory replacement cannot orphan deferred sibling", []manifest.Entry{dir("shape"), file("shape/child"), file("shape/sibling")}, []manifest.Entry{file("shape")}, []string{"shape/child"}, false, nil, nil},
		{"directory replacement selects entire affected subtree", []manifest.Entry{dir("shape"), file("shape/child"), file("shape/sibling")}, []manifest.Entry{file("shape")}, []string{"shape"}, true, []string{"shape", "shape/child", "shape/sibling"}, []string{}},
		{"directory deletion cannot orphan deferred sibling", []manifest.Entry{dir("shape"), file("shape/child"), file("shape/sibling")}, []manifest.Entry{}, []string{"shape/child"}, false, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, selection, err := selectedDelta(history.Snapshot{Profile: manifest.Profile, Entries: tc.base}, history.Snapshot{Profile: manifest.Profile, Entries: tc.full}, b, tc.roots)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid structural subset accepted", snapshot)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(selection.SelectedPaths, tc.paths) || !reflect.DeepEqual(selection.DeferredPaths, tc.deferred) {
				t.Fatal(selection, err)
			}
		})
	}
}

func TestPartialCompositionStillChecksOriginalSourceHead(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	l := library(t, s)
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := composeBranch(t, s, l.ID, main.CommitID, "source", map[string]string{"selected": "selected", "deferred": "deferred"})
	prepared, err := s.ComposeMerge(ctx, testActor, l.ID, ComposeInput{RequestID: "partial", ExpectedMain: main.CommitID, ExpectedPolicyRevision: 1, Branches: []CompositionBranch{{BranchID: source.ID, ExpectedHead: source.Head, SelectedPaths: []string{"selected"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"approve", prepared.Digest, "approve", ""}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveBranchText(ctx, testActor, l.ID, source.ID, SaveBranchTextInput{"advance", source.DraftRevision, source.Head, "deferred", "new deferred bytes"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(ctx, testActor, l.ID, source.ID, CheckpointInput{"advance-commit", source.Head, source.DraftRevision + 1, "Advance source"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, testActor, l.ID, MergeInput{"stale", prepared.Digest, main.CommitID}); err != ErrConflict {
		t.Fatal("candidate parent masked original source advancement", err)
	}
}
