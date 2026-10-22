package authoring

import (
	"context"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"

	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

func revertSnapshot(t *testing.T, s *Store, library string, parents []string, entries []manifest.Entry, blobs map[string]string) string {
	t.Helper()
	r, err := s.historyRepository(context.Background(), library)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.Commit(context.Background(), history.Snapshot{Profile: manifest.Profile, Entries: entries}, parents, "Revert fixture", func(path string) (io.ReadCloser, error) {
		for _, entry := range entries {
			if entry.Path == path {
				return io.NopCloser(strings.NewReader(blobs[entry.Digest])), nil
			}
		}
		return nil, ErrNotFound
	})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}
func revertFile(path, value string) manifest.Entry {
	return manifest.Entry{Path: path, Kind: "file", Size: strconv.Itoa(len(value)), Digest: digestBytes([]byte(value))}
}

func TestReviewedRevertPreservesConcurrentWorkAndRequiresFreshReview(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	l := library(t, s)
	root, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	binary := "\x00\xfforiginal\x01"
	parent := revertSnapshot(t, s, l.ID, []string{root.CommitID}, []manifest.Entry{revertFile("shape", binary)}, map[string]string{digestBytes([]byte(binary)): binary})
	target := revertSnapshot(t, s, l.ID, []string{parent}, []manifest.Entry{{Path: "shape", Kind: "directory", Size: "0"}, revertFile("shape/new.txt", "introduced")}, map[string]string{digestBytes([]byte("introduced")): "introduced"})
	accept := func(b Branch, expected, request string) string {
		t.Helper()
		p, e := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{RequestID: request + "-prepare", BranchID: b.ID, ExpectedHead: b.Head, ExpectedMain: expected, ExpectedPolicyRevision: 1})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Merge(ctx, testActor, l.ID, MergeInput{request + "-unreviewed", p.Digest, expected}); e == nil {
			t.Fatal("unreviewed merge accepted")
		}
		if _, e = s.Review(ctx, testActor, l.ID, ReviewInput{request + "-approve", p.Digest, "approve", "Reviewed exact inverse"}); e != nil {
			t.Fatal(e)
		}
		m, e := s.Merge(ctx, testActor, l.ID, MergeInput{request + "-merge", p.Digest, expected})
		if e != nil {
			t.Fatal(e)
		}
		return m.AcceptedCommit
	}
	source, err := s.CreateBranch(ctx, testActor, l.ID, CreateBranchInput{"source", "Original", target})
	if err != nil {
		t.Fatal(err)
	}
	accepted := accept(source, root.CommitID, "original")
	human := composeBranch(t, s, l.ID, accepted, "human", map[string]string{"human.txt": "keep human work"})
	current := accept(human, accepted, "human")
	input := RevertBranchInput{"revert", "Inverse", target, parent}
	inverse, err := s.RevertBranch(ctx, testActor, l.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if inverse.Head != target || inverse.InitialBase != target || inverse.DraftRevision != 1 {
		t.Fatal(inverse)
	}
	replay, err := s.RevertBranch(ctx, testActor, l.ID, input)
	if err != nil || replay != inverse {
		t.Fatal(replay, err)
	}
	f, _, err := s.BranchContent(ctx, testActor, l.ID, inverse.ID, "shape", 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != binary {
		t.Fatal("inverse binary changed", data, err)
	}
	unchanged, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil || unchanged.CommitID != current {
		t.Fatal("creation moved main", unchanged, err)
	}
	checkpoint, err := s.Checkpoint(ctx, testActor, l.ID, inverse.ID, CheckpointInput{"inverse-commit", target, 1, "Undo selected commit"})
	if err != nil {
		t.Fatal(err)
	}
	inverse.Head = checkpoint.CommitID
	final := accept(inverse, current, "inverse")
	r, _ := s.historyRepository(ctx, l.ID)
	for path, want := range map[string]string{"shape": binary, "human.txt": "keep human work"} {
		got, e := r.ReadFile(ctx, final, path)
		if e != nil || string(got) != want {
			t.Fatal(path, string(got), e)
		}
	}
	snapshot, err := r.Snapshot(ctx, final)
	if err != nil || len(snapshot.Entries) != 2 {
		t.Fatal("introduced child survived", snapshot, err)
	}
	version, err := s.Release(ctx, testActor, l.ID, ReleaseInput{RequestID: "revert-release", AcceptedCommit: final, Label: "Reviewed inverse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ValidateNext(ctx); err != nil {
		t.Fatal(err)
	}
	f, _, err = s.Content(ctx, l.ID, "shape", version.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != binary {
		t.Fatal("released inverse differs", data, err)
	}
	original, err := r.Snapshot(ctx, target)
	if err != nil || len(original.Entries) != 2 || original.Entries[0].Kind != "directory" {
		t.Fatal("history rewritten", original, err)
	}
}

func TestRevertExplicitMergeParentAndAuthority(t *testing.T) {
	s, _ := testStore(t)
	authority := &branchAuthority{true}
	s.config.Authority = authority
	ctx := context.Background()
	l := library(t, s)
	root, _ := s.MainCommit(ctx, testActor, l.ID)
	left := composeBranch(t, s, l.ID, root.CommitID, "left", map[string]string{"left": "left"})
	right := composeBranch(t, s, l.ID, root.CommitID, "right", map[string]string{"right": "right"})
	target := revertSnapshot(t, s, l.ID, []string{left.Head, right.Head}, []manifest.Entry{revertFile("left", "left"), revertFile("right", "right")}, map[string]string{digestBytes([]byte("left")): "left", digestBytes([]byte("right")): "right"})
	for index, parent := range []string{left.Head, right.Head} {
		input := RevertBranchInput{strconv.Itoa(index), "Inverse " + strconv.Itoa(index), target, parent}
		b, err := s.RevertBranch(ctx, testActor, l.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := s.historyRepository(ctx, l.ID)
		want, _ := r.Snapshot(ctx, parent)
		got, err := s.BranchTree(ctx, testActor, l.ID, b.ID, "", "", 1)
		if err != nil || !reflect.DeepEqual(got.Entries, want.Entries) {
			t.Fatal(got, want, err)
		}
		changed := input
		changed.Parent = root.CommitID
		if _, err = s.RevertBranch(ctx, testActor, l.ID, changed); err != ErrConflict {
			t.Fatal("changed replay accepted", err)
		}
		authority.allowed = false
		if _, err = s.RevertBranch(ctx, testActor, l.ID, input); err != ErrAuthority {
			t.Fatal("revoked replay accepted", err)
		}
		authority.allowed = true
	}
	foreign, err := s.Create(ctx, testActor, CreateInput{RequestID: "foreign-library", Name: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	foreignRoot, _ := s.MainCommit(ctx, testActor, foreign.ID)
	// Unique foreign history; empty roots can intentionally share content identity.
	foreignBranch := composeBranch(t, s, foreign.ID, foreignRoot.CommitID, "foreign", map[string]string{"foreign": "foreign"})
	for _, pair := range [][2]string{{target, root.CommitID}, {root.CommitID, left.Head}, {target, ""}, {target, foreignBranch.Head}, {foreignBranch.Head, foreignRoot.CommitID}} {
		if _, err := s.RevertBranch(ctx, testActor, l.ID, RevertBranchInput{randomID(), randomID(), pair[0], pair[1]}); err == nil {
			t.Fatal("invalid ancestry accepted", pair)
		}
	}
}

func TestRevertBranchCrashRecovery(t *testing.T) {
	for _, point := range []string{"after_intent", "after_ref"} {
		t.Run(point, func(t *testing.T) {
			s, cfg := testStore(t)
			authority := &branchAuthority{true}
			s.config.Authority = authority
			cfg.Authority = authority
			ctx := context.Background()
			l := library(t, s)
			root, _ := s.MainCommit(ctx, testActor, l.ID)
			source := composeBranch(t, s, l.ID, root.CommitID, "source", map[string]string{"added": "new"})
			s.branchCrash = func(p string) error {
				if p == point {
					return ErrBusy
				}
				return nil
			}
			input := RevertBranchInput{"inverse", "Inverse", source.Head, root.CommitID}
			original, err := s.RevertBranch(ctx, testActor, l.ID, input)
			if err == nil {
				t.Fatal("fault missed")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			b, err := reopened.RevertBranch(ctx, testActor, l.ID, input)
			if err != nil || b != original {
				t.Fatal(b, original, err)
			}
			ready, err := reopened.Branch(ctx, testActor, l.ID, b.ID)
			if err != nil || ready.Head != source.Head {
				t.Fatal(ready, err)
			}
			draft, err := reopened.BranchTree(ctx, testActor, l.ID, b.ID, "", "", 1)
			if err != nil || len(draft.Entries) != 0 {
				t.Fatal("inverse draft lost", draft, err)
			}
		})
	}
}

func TestRevertConflictsWithConcurrentEditAndRespectsArchive(t *testing.T) {
	s, _ := testStore(t)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	l := library(t, s)
	root, _ := s.MainCommit(ctx, testActor, l.ID)
	accept := func(branch Branch, main, request string) string {
		t.Helper()
		p, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{request + "-prepare", branch.ID, branch.Head, main, 1, ""})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{request + "-review", p.Digest, "approve", ""}); err != nil {
			t.Fatal(err)
		}
		m, err := s.Merge(ctx, testActor, l.ID, MergeInput{request + "-merge", p.Digest, main})
		if err != nil {
			t.Fatal(err)
		}
		return m.AcceptedCommit
	}
	source := composeBranch(t, s, l.ID, root.CommitID, "original", map[string]string{"note": "original"})
	target := accept(source, root.CommitID, "source")
	human := composeBranch(t, s, l.ID, target, "human", map[string]string{"note": "later human edit"})
	main := accept(human, target, "human")
	b, err := s.RevertBranch(ctx, testActor, l.ID, RevertBranchInput{"inverse", "Inverse", target, root.CommitID})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Checkpoint(ctx, testActor, l.ID, b.ID, CheckpointInput{"inverse-commit", b.Head, 1, "Undo original addition"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{"inverse-prepare", b.ID, c.CommitID, main, 1, ""})
	if err != nil || len(p.Conflicts) == 0 || p.ConflictWorkspaceID == "" {
		t.Fatal("concurrent edit silently lost", p, err)
	}
	current, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil || current.CommitID != main {
		t.Fatal(current, err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE libraries SET archived=1 WHERE id=?", l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevertBranch(ctx, testActor, l.ID, RevertBranchInput{"archived", "Archived inverse", target, root.CommitID}); err != ErrConflict {
		t.Fatal("archived inverse accepted", err)
	}
}
