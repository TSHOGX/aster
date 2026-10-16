package authoring

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

// CompositionBranch freezes one branch head and the changes selected from it.
// An empty selection means all changes in that branch's committed delta.
type CompositionBranch struct {
	BranchID      string   `json:"branch_id"`
	ExpectedHead  string   `json:"expected_head"`
	SelectedPaths []string `json:"selected_paths,omitempty"`
}

type ComposeInput struct {
	RequestID              string              `json:"request_id"`
	ExpectedMain           string              `json:"expected_main"`
	ExpectedPolicyRevision int64               `json:"expected_policy_revision"`
	Branches               []CompositionBranch `json:"branches"`
}

type CompositionSelection struct {
	CandidateCommit string   `json:"candidate_commit"`
	BranchID        string   `json:"branch_id"`
	Head            string   `json:"head"`
	SelectedPaths   []string `json:"selected_paths"`
	DeferredPaths   []string `json:"deferred_paths,omitempty"`
}

type CompositionResult struct {
	PreparedResult
	Selections []CompositionSelection `json:"selections"`
}

type compositionSource struct {
	branch    Branch
	base      history.Snapshot
	full      history.Snapshot
	selected  history.Snapshot
	selection CompositionSelection
}

// ComposeMerge prepares one same-Volume integration from multiple immutable
// branch heads. Each branch contributes only its selected committed changes;
// omitted changes remain deferred on the source branch and are never claimed as
// part of the prepared result.
func (s *Store) ComposeMerge(ctx context.Context, a Actor, library string, input ComposeInput) (CompositionResult, error) {
	var result CompositionResult
	if !manifest.ValidID(library) || !evolution.ObjectID(input.ExpectedMain) || input.ExpectedPolicyRevision < 1 || len(input.Branches) == 0 || len(input.Branches) > 16 {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	body := requestDigest("merge.compose", library, input)
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	libraryRecord, err := s.Library(ctx, library)
	if err != nil {
		return result, err
	}
	if libraryRecord.Archived {
		return result, ErrConflict
	}
	var policy string
	var revision int64
	if err = s.db.QueryRowContext(ctx, "SELECT revision,digest FROM library_merge_policies WHERE library_id=?", library).Scan(&revision, &policy); err != nil {
		return result, normalizeDB(err)
	}
	if revision != input.ExpectedPolicyRevision {
		return result, ErrConflict
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil || main != input.ExpectedMain {
		if err == nil {
			err = ErrConflict
		}
		return result, err
	}
	mainTree, err := repo.Snapshot(ctx, main)
	if err != nil {
		return result, err
	}

	sources := make([]compositionSource, 0, len(input.Branches))
	seenBranches := map[string]bool{}
	for _, requested := range input.Branches {
		if !manifest.ValidID(requested.BranchID) || !evolution.ObjectID(requested.ExpectedHead) || seenBranches[requested.BranchID] {
			return result, ErrInvalid
		}
		seenBranches[requested.BranchID] = true
		branch, branchErr := s.branch(ctx, library, requested.BranchID)
		if branchErr != nil {
			return result, branchErr
		}
		if branch.Head != requested.ExpectedHead || branch.Head == main {
			return result, ErrConflict
		}
		base, branchErr := repo.Snapshot(ctx, branch.InitialBase)
		if branchErr != nil {
			return result, branchErr
		}
		full, branchErr := repo.Snapshot(ctx, branch.Head)
		if branchErr != nil {
			return result, branchErr
		}
		selected, selection, branchErr := selectedDelta(base, full, branch, requested.SelectedPaths)
		if branchErr != nil {
			return result, branchErr
		}
		if len(selection.SelectedPaths) == 0 {
			continue
		}
		sources = append(sources, compositionSource{branch: branch, base: base, full: full, selected: selected, selection: selection})
	}
	if len(sources) == 0 {
		return result, ErrInvalid
	}

	current := mainTree
	conflicts := []history.Conflict{}
	for _, source := range sources {
		merged, found, mergeErr := history.Merge(source.base, current, source.selected)
		if mergeErr != nil {
			return result, mergeErr
		}
		if len(found) > 0 {
			conflicts = append(conflicts, found...)
			continue
		}
		current = merged
	}
	if len(conflicts) > 0 {
		result.Conflicts = conflicts
		result.Selections = selectionsFromSources(sources)
		return result, ErrConflict
	}

	locations := map[string]string{}
	allSnapshots := []struct {
		commit string
		tree   history.Snapshot
	}{{main, mainTree}}
	for _, source := range sources {
		allSnapshots = append(allSnapshots, struct {
			commit string
			tree   history.Snapshot
		}{source.branch.Head, source.full})
	}
	for _, snapshot := range allSnapshots {
		for _, entry := range snapshot.tree.Entries {
			if entry.Kind == "file" {
				locations[entry.Path+"\x00"+entry.Digest] = snapshot.commit
			}
		}
	}
	if err = s.materializeSnapshot(ctx, repo, library, current, locations); err != nil {
		return result, err
	}
	descriptor, err := manifest.NewProfile(s.config.PublisherID, library, "integration", current.Profile, current.Entries)
	if err != nil {
		return result, err
	}
	if err = s.validateProfileContent(descriptor); err != nil {
		return result, err
	}
	for i := range sources {
		source := &sources[i]
		source.selection.CandidateCommit = source.branch.Head
		if len(source.selection.DeferredPaths) > 0 {
			// The full head must not become an ancestor of accepted main: doing so
			// would make ordinary reprepare treat deferred changes as consumed.
			locations := map[string]string{}
			for _, entry := range source.base.Entries {
				locations[entry.Path+"\x00"+entry.Digest] = source.branch.InitialBase
			}
			for _, entry := range source.full.Entries {
				locations[entry.Path+"\x00"+entry.Digest] = source.branch.Head
			}
			if err = s.materializeSnapshot(ctx, repo, library, source.selected, locations); err != nil {
				return result, err
			}
			byPath := map[string]manifest.Entry{}
			for _, entry := range source.selected.Entries {
				byPath[entry.Path] = entry
			}
			source.selection.CandidateCommit, err = repo.Commit(ctx, source.selected, []string{source.branch.InitialBase}, "Select changes from branch "+source.branch.ID+" at "+source.branch.Head, func(path string) (io.ReadCloser, error) {
				return os.OpenFile(s.blobPath(byPath[path].Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			})
			if err != nil {
				return result, err
			}
		}
	}

	byPath := map[string]manifest.Entry{}
	for _, entry := range current.Entries {
		byPath[entry.Path] = entry
	}
	parents := []string{main}
	for _, source := range sources {
		parents = append(parents, source.selection.CandidateCommit)
	}
	commit, err := repo.Commit(ctx, current, parents, "Prepare composed integration", func(path string) (io.ReadCloser, error) {
		return os.OpenFile(s.blobPath(byPath[path].Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	})
	if err != nil {
		return result, err
	}
	mainRef := evolution.ContentRef{LibraryID: library, CommitID: main, Profile: mainTree.Profile, TreeDigest: mainTree.TreeDigest()}
	sourcesRef := make([]evolution.ContentRef, 0, len(sources))
	checks := []evolution.CheckRequirement{}
	for _, source := range sources {
		sourcesRef = append(sourcesRef, evolution.ContentRef{LibraryID: library, CommitID: source.branch.Head, Profile: source.full.Profile, TreeDigest: source.full.TreeDigest()})
		branchChecks, checkErr := s.preparedEvaluationChecks(ctx, library, source.branch, mainRef, current)
		if checkErr != nil {
			return result, checkErr
		}
		checks = append(checks, branchChecks...)
	}
	checks, err = evolution.UnionChecks(checks)
	if err != nil {
		return result, err
	}
	identity := evolution.PreparedIdentity{LibraryID: library, Main: mainRef, Sources: sourcesRef, Result: evolution.ContentRef{LibraryID: library, CommitID: commit, Profile: current.Profile, TreeDigest: current.TreeDigest()}, Selections: identitySelectionsFromSources(sources), PolicyRevision: revision, PolicyDigest: policy, Checks: checks}
	result.Digest, err = identity.ApprovalDigest()
	if err != nil {
		return result, err
	}
	result.Identity = &identity
	result.Selections = selectionsFromSources(sources)
	raw, err := json.Marshal(identity)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO prepared_merges VALUES(?,?,?,0,?)", result.Digest, library, raw, s.now().Unix()); err != nil {
		return result, err
	}
	for _, source := range sources {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO prepared_sources VALUES(?,?,?)", result.Digest, source.branch.ID, source.branch.Head); err != nil {
			return result, err
		}
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "merge.compose", library, revision, result); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if err = s.pinPrepared(ctx, library, result.PreparedResult); err != nil {
		s.poisoned = true
		return result, err
	}
	return result, nil
}

func selectedDelta(base, full history.Snapshot, branch Branch, requested []string) (history.Snapshot, CompositionSelection, error) {
	if base.Profile != full.Profile || branch.InitialBase == "" {
		return history.Snapshot{}, CompositionSelection{}, ErrInvalid
	}
	changes := compareEntries(base.Entries, full.Entries)
	changed := map[string]bool{}
	for _, change := range changes {
		changed[change.Path] = true
	}
	selectedRoots := append([]string{}, requested...)
	for _, path := range selectedRoots {
		if manifest.ValidPath(path, true) != nil {
			return history.Snapshot{}, CompositionSelection{}, ErrInvalid
		}
	}
	if len(selectedRoots) == 0 {
		for _, change := range changes {
			selectedRoots = append(selectedRoots, change.Path)
		}
	}
	selectedSet := map[string]bool{}
	for _, change := range changes {
		for _, root := range selectedRoots {
			if change.Path == root || strings.HasPrefix(change.Path, root+"/") || strings.HasPrefix(root, change.Path+"/") {
				selectedSet[change.Path] = true
				break
			}
		}
	}
	if len(selectedSet) == 0 {
		return history.Snapshot{}, CompositionSelection{}, ErrInvalid
	}
	baseEntries := map[string]manifest.Entry{}
	fullEntries := map[string]manifest.Entry{}
	for _, entry := range base.Entries {
		baseEntries[entry.Path] = entry
	}
	for _, entry := range full.Entries {
		fullEntries[entry.Path] = entry
	}
	for path := range selectedSet {
		if entry, ok := fullEntries[path]; ok {
			baseEntries[path] = entry
		} else {
			delete(baseEntries, path)
		}
	}
	entries := make([]manifest.Entry, 0, len(baseEntries))
	for _, entry := range baseEntries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	selected := history.Snapshot{Profile: base.Profile, Entries: entries}
	if err := selected.Validate(); err != nil {
		return history.Snapshot{}, CompositionSelection{}, err
	}
	selectedPaths := make([]string, 0, len(selectedSet))
	deferred := make([]string, 0, len(changed)-len(selectedSet))
	for path := range changed {
		if selectedSet[path] {
			selectedPaths = append(selectedPaths, path)
		} else {
			deferred = append(deferred, path)
		}
	}
	sort.Strings(selectedPaths)
	sort.Strings(deferred)
	return selected, CompositionSelection{BranchID: branch.ID, Head: branch.Head, SelectedPaths: selectedPaths, DeferredPaths: deferred}, nil
}

func identitySelectionsFromSources(sources []compositionSource) []evolution.PreparedSelection {
	result := make([]evolution.PreparedSelection, 0, len(sources))
	for _, source := range sources {
		result = append(result, evolution.PreparedSelection{CandidateCommit: source.selection.CandidateCommit, BranchID: source.selection.BranchID, Head: source.selection.Head, SelectedPaths: append([]string{}, source.selection.SelectedPaths...), DeferredPaths: append([]string{}, source.selection.DeferredPaths...)})
	}
	return result
}

func selectionsFromSources(sources []compositionSource) []CompositionSelection {
	result := make([]CompositionSelection, 0, len(sources))
	for _, source := range sources {
		result = append(result, CompositionSelection{CandidateCommit: source.selection.CandidateCommit, BranchID: source.selection.BranchID, Head: source.selection.Head, SelectedPaths: append([]string{}, source.selection.SelectedPaths...), DeferredPaths: append([]string{}, source.selection.DeferredPaths...)})
	}
	return result
}

func (s *Store) materializeSnapshot(ctx context.Context, repo *history.Repository, _ string, snapshot history.Snapshot, locations map[string]string) error {
	for _, entry := range snapshot.Entries {
		if entry.Kind != "file" {
			continue
		}
		commit := locations[entry.Path+"\x00"+entry.Digest]
		if commit == "" {
			return ErrInvalid
		}
		if err := s.materialize(ctx, entry, func(w io.Writer) error { return repo.ExportFile(ctx, commit, entry.Path, w) }); err != nil {
			return err
		}
	}
	return nil
}
