package authoring

import (
	"context"
	"encoding/json"
	"strings"

	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

type ConflictWorkspace struct {
	Resolutions    []ConflictResolution `json:"resolutions"`
	ID             string               `json:"id"`
	LibraryID      string               `json:"library_id"`
	BranchID       string               `json:"branch_id"`
	BaseCommit     string               `json:"base_commit"`
	MainCommit     string               `json:"main_commit"`
	SourceCommit   string               `json:"source_commit"`
	PolicyRevision int64                `json:"policy_revision"`
	PolicyDigest   string               `json:"policy_digest"`
	Conflicts      []history.Conflict   `json:"conflicts"`
	Revision       int64                `json:"revision"`
	State          string               `json:"state"`
	Stale          bool                 `json:"stale"`
}

func (s *Store) ConflictWorkspace(ctx context.Context, a Actor, library, id string) (ConflictWorkspace, error) {
	var workspace ConflictWorkspace
	if !manifest.ValidID(id) {
		return workspace, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return workspace, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conflictWorkspace(ctx, library, id)
}
func (s *Store) conflictWorkspace(ctx context.Context, library, id string) (ConflictWorkspace, error) {
	var workspace ConflictWorkspace
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT id,library_id,branch_id,base_commit,main_commit,source_commit,policy_revision,policy_digest,conflicts,revision,state FROM conflict_workspaces WHERE library_id=? AND id=?", library, id).Scan(&workspace.ID, &workspace.LibraryID, &workspace.BranchID, &workspace.BaseCommit, &workspace.MainCommit, &workspace.SourceCommit, &workspace.PolicyRevision, &workspace.PolicyDigest, &raw, &workspace.Revision, &workspace.State)
	if err != nil {
		return workspace, normalizeDB(err)
	}
	if err = json.Unmarshal(raw, &workspace.Conflicts); err != nil {
		return workspace, err
	}
	branch, err := s.branch(ctx, library, workspace.BranchID)
	if err != nil {
		return workspace, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return workspace, err
	}
	main, err := repo.Head(ctx, "refs/heads/main")
	if err != nil {
		return workspace, err
	}
	var revision int64
	var digest string
	if err = s.db.QueryRowContext(ctx, "SELECT revision,digest FROM library_merge_policies WHERE library_id=?", library).Scan(&revision, &digest); err != nil {
		return workspace, err
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return workspace, err
	}
	workspace.Stale = l.Archived || branch.Head != workspace.SourceCommit || main != workspace.MainCommit || revision != workspace.PolicyRevision || digest != workspace.PolicyDigest
	rows, err := s.db.QueryContext(ctx, "SELECT path,choice,text FROM conflict_resolutions WHERE workspace_id=? ORDER BY path", id)
	if err != nil {
		return workspace, err
	}
	defer rows.Close()
	workspace.Resolutions = []ConflictResolution{}
	for rows.Next() {
		var resolution ConflictResolution
		if err = rows.Scan(&resolution.Path, &resolution.Choice, &resolution.Text); err != nil {
			return workspace, err
		}
		workspace.Resolutions = append(workspace.Resolutions, resolution)
	}
	return workspace, rows.Err()
}

type ConflictResolutionInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Path             string `json:"path"`
	Choice           string `json:"choice"`
	Text             string `json:"text"`
}
type ConflictResolution struct {
	Path   string `json:"path"`
	Choice string `json:"choice"`
	Text   string `json:"text"`
}

func (s *Store) SaveConflictResolution(ctx context.Context, a Actor, library, id string, input ConflictResolutionInput) (MutationResult, error) {
	var result MutationResult
	if input.ExpectedRevision < 1 || manifest.ValidPath(input.Path, false) != nil || !validText(input.Text, MaxTextBytes) {
		return result, ErrInvalid
	}
	if input.Choice != "main" && input.Choice != "branch" && input.Choice != "delete" && input.Choice != "text" {
		return result, ErrInvalid
	}
	if input.Choice != "text" && input.Text != "" {
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
	body := requestDigest("conflict.resolve", library, struct {
		Workspace string
		Input     ConflictResolutionInput
	}{id, input})
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	workspace, err := s.conflictWorkspace(ctx, library, id)
	if err != nil {
		return result, err
	}
	if workspace.Stale || workspace.State != "open" || workspace.Revision != input.ExpectedRevision {
		return result, ErrConflict
	}
	found := false
	for _, conflict := range workspace.Conflicts {
		if conflict.Path == input.Path {
			found = true
			break
		}
	}
	if !found {
		return result, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO conflict_resolutions VALUES(?,?,?,?) ON CONFLICT(workspace_id,path) DO UPDATE SET choice=excluded.choice,text=excluded.text", id, input.Path, input.Choice, input.Text); err != nil {
		return result, err
	}
	result = MutationResult{Revision: workspace.Revision + 1, Affected: 1}
	if _, err = tx.ExecContext(ctx, "UPDATE conflict_workspaces SET revision=? WHERE id=?", result.Revision, id); err != nil {
		return result, err
	}
	if err = s.record(ctx, tx, a, input.RequestID, body, "conflict.resolve", library, result.Revision, result); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

type CompleteConflictInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *Store) CompleteConflict(ctx context.Context, a Actor, library, id string, input CompleteConflictInput) (PreparedResult, error) {
	var result PreparedResult
	if input.ExpectedRevision < 1 {
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
	body := requestDigest("conflict.complete", library, struct {
		Workspace string
		Input     CompleteConflictInput
	}{id, input})
	if replay, err := readReceipt(ctx, s.db.DB, a, input.RequestID, body, &result); err != nil || replay {
		return result, err
	}
	workspace, err := s.conflictWorkspace(ctx, library, id)
	if err != nil {
		return result, err
	}
	if workspace.Stale || workspace.State != "open" || workspace.Revision != input.ExpectedRevision {
		return result, ErrConflict
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return result, err
	}
	merged, conflicts, content, err := repo.MergeCommits(ctx, workspace.BaseCommit, workspace.MainCommit, workspace.SourceCommit)
	if err != nil {
		return result, err
	}
	main, err := repo.Snapshot(ctx, workspace.MainCommit)
	if err != nil {
		return result, err
	}
	source, err := repo.Snapshot(ctx, workspace.SourceCommit)
	if err != nil {
		return result, err
	}
	resolved, text, err := resolveConflictTree(merged, main, source, conflicts, workspace.Resolutions)
	if err != nil {
		return result, err
	}
	content = resolvedContent(resolved, content, text)
	branch, err := s.branch(ctx, library, workspace.BranchID)
	if err != nil {
		return result, err
	}
	return s.persistPrepared(ctx, a, library, input.RequestID, body, branch, workspace.MainCommit, workspace.PolicyRevision, workspace.PolicyDigest, resolved, content, id, "")
}

func (s *Store) ConflictFile(ctx context.Context, a Actor, library, id, side, path string) (IntegrationFile, error) {
	var file IntegrationFile
	if manifest.ValidPath(path, false) != nil {
		return file, ErrInvalid
	}
	workspace, err := s.ConflictWorkspace(ctx, a, library, id)
	if err != nil {
		return file, err
	}
	commit := ""
	switch side {
	case "base":
		commit = workspace.BaseCommit
	case "main":
		commit = workspace.MainCommit
	case "branch":
		commit = workspace.SourceCommit
	default:
		return file, ErrInvalid
	}
	allowed := false
	for _, conflict := range workspace.Conflicts {
		if path == conflict.Path || strings.HasPrefix(path, conflict.Path+"/") {
			allowed = true
			break
		}
	}
	if !allowed {
		return file, ErrInvalid
	}
	file, err = s.CommitFile(ctx, a, library, commit, path)
	if err == ErrNotFound && file.CommitID == commit {
		file.Preview = "absent"
		return file, nil
	}
	return file, err
}
