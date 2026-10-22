package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"syscall"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

type PrepareInput struct {
	RequestID              string `json:"request_id"`
	BranchID               string `json:"branch_id"`
	ExpectedHead           string `json:"expected_head"`
	ExpectedMain           string `json:"expected_main"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
	// RebindPreparedDigest explicitly replaces retained requirements from an
	// earlier open preparation; policy relaxation never does this implicitly.
	RebindPreparedDigest string `json:"rebind_prepared_digest,omitempty"`
}
type PreparedResult struct {
	ConflictWorkspaceID string                      `json:"conflict_workspace_id,omitempty"`
	Digest              string                      `json:"digest"`
	Identity            *evolution.PreparedIdentity `json:"identity,omitempty"`
	Conflicts           []history.Conflict          `json:"conflicts"`
}

func (s *Store) PrepareMerge(ctx context.Context, a Actor, library string, input PrepareInput) (PreparedResult, error) {
	var result PreparedResult
	if !manifest.ValidID(input.BranchID) || !evolution.ObjectID(input.ExpectedHead) || !evolution.ObjectID(input.ExpectedMain) {
		return result, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.author"); err != nil {
		return result, err
	}
	if input.RebindPreparedDigest != "" {
		if err := s.authorizeLibrary(ctx, a, library, "volume.review"); err != nil {
			return result, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	body := requestDigest("merge.prepare", library, input)
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return result, err
	}
	if l.Archived {
		return result, ErrConflict
	}
	b, err := s.branch(ctx, library, input.BranchID)
	if err != nil {
		return result, err
	}
	if b.Head != input.ExpectedHead {
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
	if err != nil {
		return result, err
	}
	if main != input.ExpectedMain || main == b.Head {
		return result, ErrConflict
	}
	retain := input.RebindPreparedDigest == ""
	if !retain {
		if !manifest.ValidDigest(input.RebindPreparedDigest) {
			return result, ErrInvalid
		}
		var oldLibrary string
		var oldRaw []byte
		if err = s.db.QueryRowContext(ctx, "SELECT library_id,identity FROM prepared_merges WHERE digest=?", input.RebindPreparedDigest).Scan(&oldLibrary, &oldRaw); err != nil {
			return result, normalizeDB(err)
		}
		if oldLibrary != library {
			return result, ErrConflict
		}
		var oldIdentity evolution.PreparedIdentity
		if json.Unmarshal(oldRaw, &oldIdentity) != nil {
			return result, ErrInvalid
		}
		oldDigest, err := oldIdentity.ApprovalDigest()
		if err != nil || oldDigest != input.RebindPreparedDigest {
			return result, ErrInvalid
		}
		if len(oldIdentity.Selections) != 0 {
			return result, ErrConflict
		}
		var count, accepted int
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM prepared_sources WHERE prepared_digest=?", input.RebindPreparedDigest).Scan(&count); err != nil {
			return result, err
		}
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM merge_operations WHERE prepared_digest=?", input.RebindPreparedDigest).Scan(&accepted); err != nil {
			return result, err
		}
		if count != 1 || accepted != 0 {
			return result, ErrConflict
		}
		var sourceBranch, sourceHead string
		if err = s.db.QueryRowContext(ctx, "SELECT branch_id,head FROM prepared_sources WHERE prepared_digest=? ORDER BY branch_id LIMIT 1", input.RebindPreparedDigest).Scan(&sourceBranch, &sourceHead); err != nil {
			return result, normalizeDB(err)
		}
		if sourceBranch != b.ID || sourceHead != b.Head {
			return result, ErrConflict
		}
	}
	base, err := repo.MergeBase(ctx, main, b.Head)
	if err != nil {
		return result, err
	}
	tree, conflicts, merged, err := repo.MergeCommits(ctx, base, main, b.Head)
	if err != nil {
		return result, err
	}
	result.Conflicts = conflicts
	if len(conflicts) > 0 {
		result.ConflictWorkspaceID = randomID()
		raw, err := json.Marshal(conflicts)
		if err != nil {
			return result, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return result, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "INSERT INTO conflict_workspaces(id,library_id,branch_id,base_commit,main_commit,source_commit,policy_revision,policy_digest,conflicts,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,'open',?)", result.ConflictWorkspaceID, library, b.ID, base, main, b.Head, revision, policy, raw, s.now().Unix()); err != nil {
			return result, err
		}
		if err = s.record(ctx, tx, a, input.RequestID, body, "merge.conflicted", library, revision, result); err != nil {
			return result, err
		}
		if err = tx.Commit(); err != nil {
			return result, err
		}
		if err = settle(ctx, func(ctx context.Context) error {
			return s.pinConflictInputs(ctx, library, result.ConflictWorkspaceID, base, main, b.Head)
		}); err != nil {
			s.poisoned = true
			return result, err
		}
		return result, nil
	}

	return s.persistPrepared(ctx, a, library, input.RequestID, body, b, main, revision, policy, tree, merged, "", input.RebindPreparedDigest)
}
func (s *Store) persistPrepared(ctx context.Context, a Actor, library, request, body string, b Branch, main string, revision int64, policy string, tree history.Snapshot, merged map[string][]byte, workspace, rebind string) (PreparedResult, error) {
	var result PreparedResult
	result.Conflicts = []history.Conflict{}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	mainTree, err := repo.Snapshot(ctx, main)
	if err != nil {
		return result, err
	}
	sourceTree, err := repo.Snapshot(ctx, b.Head)
	if err != nil {
		return result, err
	}
	locations := map[string]string{}
	for _, snapshot := range []struct {
		id   string
		tree history.Snapshot
	}{{main, mainTree}, {b.Head, sourceTree}} {
		for _, e := range snapshot.tree.Entries {
			locations[e.Path+"\x00"+e.Digest] = snapshot.id
		}
	}
	for _, e := range tree.Entries {
		if e.Kind != "file" {
			continue
		}
		entry := e
		if data, ok := merged[e.Path]; ok {
			err = s.materialize(ctx, e, func(w io.Writer) error { _, err := io.Copy(w, bytes.NewReader(data)); return err })
		} else {
			commit := locations[e.Path+"\x00"+e.Digest]
			if commit == "" {
				return result, ErrInvalid
			}
			err = s.materialize(ctx, e, func(w io.Writer) error { return repo.ExportFile(ctx, commit, entry.Path, w) })
		}
		if err != nil {
			return result, err
		}
	}
	descriptor, err := manifest.NewProfile(s.config.PublisherID, library, "integration", tree.Profile, tree.Entries)
	if err != nil {
		return result, err
	}
	if err = s.validateProfileContent(descriptor); err != nil {
		return result, err
	}
	byPath := map[string]manifest.Entry{}
	for _, e := range tree.Entries {
		byPath[e.Path] = e
	}
	retain := rebind == ""
	message := "Prepare reviewed integration"
	if !retain {
		message = "Rebind integration policy " + request
	}
	commit, err := repo.Commit(ctx, tree, []string{main, b.Head}, message, func(p string) (io.ReadCloser, error) {
		return os.OpenFile(s.blobPath(byPath[p].Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	})
	if err != nil {
		return result, err
	}
	ref := func(id string, snapshot history.Snapshot) evolution.ContentRef {
		return evolution.ContentRef{LibraryID: library, CommitID: id, Profile: snapshot.Profile, TreeDigest: snapshot.TreeDigest()}
	}
	checks, err := s.preparedEvaluationChecks(ctx, library, b, ref(main, mainTree), tree, retain)
	if err != nil {
		return result, err
	}
	identity := evolution.PreparedIdentity{LibraryID: library, Main: ref(main, mainTree), Sources: []evolution.ContentRef{ref(b.Head, sourceTree)}, Result: ref(commit, tree), PolicyRevision: revision, PolicyDigest: policy, Checks: checks}
	result.Digest, err = identity.ApprovalDigest()
	if err != nil {
		return result, err
	}
	result.Identity = &identity
	raw, _ := json.Marshal(identity)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO prepared_merges VALUES(?,?,?,0,?)", result.Digest, library, raw, s.now().Unix()); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO prepared_sources VALUES(?,?,?)", result.Digest, b.ID, b.Head); err != nil {
		return result, err
	}
	action := "merge.prepare"
	if !retain {
		action = "merge.policy_rebind"
		if _, err = tx.ExecContext(ctx, "INSERT INTO prepared_policy_rebinds VALUES(?,?)", result.Digest, rebind); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO prepared_policy_rebind_exclusions SELECT ?,prepared_digest FROM prepared_sources WHERE branch_id=? AND prepared_digest<>?`, result.Digest, b.ID, result.Digest); err != nil {
			return result, err
		}
	}
	if err = s.record(ctx, tx, a, request, body, action, library, revision, result); err != nil {
		return result, err
	}
	if workspace != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE conflict_workspaces SET state='resolved' WHERE id=?", workspace); err != nil {
			return result, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("prepare_after_intent"); err != nil {
			s.poisoned = true
			return result, err
		}
	}
	if err = settle(ctx, func(ctx context.Context) error { return s.pinPrepared(ctx, library, result) }); err != nil {
		s.poisoned = true
		return result, err
	}
	return result, nil
}

func (s *Store) pinPrepared(ctx context.Context, library string, result PreparedResult) error {
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	coordinator, err := history.Coordinate(ctx, repo)
	if err != nil {
		return err
	}
	defer coordinator.Close()
	op, err := coordinator.Update(ctx, "prepare-"+result.Digest[7:], "refs/prepared/"+result.Digest[7:], "", result.Identity.Result.CommitID)
	if err != nil {
		return err
	}
	if op.State != "settled" {
		return ErrConflict
	}
	return nil
}
func (s *Store) reconcilePreparedPins(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT library_id,digest,identity FROM prepared_merges ORDER BY digest")
	if err != nil {
		return err
	}
	type pending struct {
		library string
		result  PreparedResult
	}
	all := []pending{}
	for rows.Next() {
		var p pending
		var raw []byte
		if err = rows.Scan(&p.library, &p.result.Digest, &raw); err != nil {
			rows.Close()
			return err
		}
		var identity evolution.PreparedIdentity
		if err = json.Unmarshal(raw, &identity); err != nil {
			rows.Close()
			return err
		}
		computed, err := identity.ApprovalDigest()
		if err != nil || computed != p.result.Digest {
			rows.Close()
			return ErrInvalid
		}
		p.result.Identity = &identity
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		if err = s.pinPrepared(ctx, p.library, p.result); err != nil {
			return err
		}
	}
	return nil
}
