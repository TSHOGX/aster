package authoring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"volume-studio/backend/internal/history"
)

// RunResultAuthority checks current run/effect authority and the exact retained
// patch. It is distinct from human library grants and never impersonates an author.
type RunResultAuthority interface {
	AuthorizeRunResult(context.Context, evolution.RunResultScope) error
}
type RunResultArtifacts interface {
	ReadRunResultArtifact(context.Context, evolution.RunResultScope, evolution.ArtifactRef) (io.ReadCloser, error)
}

func (s *Store) resultAuthority(ctx context.Context, scope evolution.RunResultScope) error {
	if scope.Validate() != nil {
		return ErrInvalid
	}
	if s.config.RunResults == nil {
		return ErrAuthority
	}
	return s.config.RunResults.AuthorizeRunResult(ctx, scope)
}
func (s *Store) readRunResult(ctx context.Context, scope evolution.RunResultScope) (evolution.RunResultReceipt, error) {
	var receipt evolution.RunResultReceipt
	var stored []byte
	err := s.db.QueryRowContext(ctx, "SELECT scope,run_id,patch_digest,state,coalesce(branch_id,''),commit_id FROM run_results WHERE group_id=? AND run_id=?", scope.GroupID, scope.RunID).Scan(&stored, &receipt.RunID, &receipt.PatchDigest, &receipt.State, &receipt.BranchID, &receipt.CommitID)
	if err != nil {
		return receipt, normalizeDB(err)
	}
	expected, _ := json.Marshal(scope)
	if !bytes.Equal(expected, stored) {
		return evolution.RunResultReceipt{}, ErrConflict
	}
	return receipt, nil
}

// RunResult returns a durable acceptance receipt under current exact run authority.
// Recovery finishes before the store opens, so prepared state is never success.
func (s *Store) RunResult(ctx context.Context, scope evolution.RunResultScope) (evolution.RunResultReceipt, error) {
	if err := s.resultAuthority(ctx, scope); err != nil {
		return evolution.RunResultReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return evolution.RunResultReceipt{}, ErrBusy
	}
	receipt, err := s.readRunResult(ctx, scope)
	if err == nil && receipt.State == "prepared" {
		return evolution.RunResultReceipt{}, ErrBusy
	}
	return receipt, err
}

// SubmitRunResult materializes only verified scoped artifacts and commits a new
// isolated branch. It never edits main, merges, publishes or rewrites a run result.
// The accepted intent, draft pins and lineage precede the Git ref side effect.
func (s *Store) SubmitRunResult(ctx context.Context, scope evolution.RunResultScope, patch evolution.Patch) (evolution.RunResultReceipt, error) {
	var receipt evolution.RunResultReceipt
	if scope.Matches(patch) != nil {
		return receipt, ErrInvalid
	}
	if err := s.resultAuthority(ctx, scope); err != nil {
		return receipt, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return receipt, ErrBusy
	}
	receipt, err := s.readRunResult(ctx, scope)
	if err == nil {
		if receipt.State == "prepared" {
			return receipt, ErrBusy
		}
		return receipt, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return receipt, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return receipt, err
	}
	err = checkEvolutionOutput(ctx, tx, scope.GroupID, scope.BindingID, scope.Output)
	_ = tx.Rollback()
	if err != nil {
		return receipt, err
	}
	repo, err := s.historyRepository(ctx, scope.Output.LibraryID)
	if err != nil {
		return receipt, err
	}
	base, err := repo.Snapshot(ctx, scope.Base.CommitID)
	if err != nil {
		return receipt, err
	}
	if base.Profile != scope.Base.Profile || base.TreeDigest() != scope.Base.TreeDigest {
		return receipt, ErrConflict
	}
	tree, err := patch.ApplyTree(scope.Output, base.Entries)
	if err != nil {
		return receipt, errors.Join(ErrInvalid, err)
	}
	noChange := manifest.TreeDigest(tree) == base.TreeDigest()
	receipt = evolution.RunResultReceipt{RunID: scope.RunID, PatchDigest: scope.PatchDigest, State: "no_change", CommitID: scope.Base.CommitID}
	if !noChange {
		descriptor, err := manifest.NewProfile(s.config.PublisherID, scope.Output.LibraryID, "run-result", scope.Output.Profile, tree)
		if err != nil {
			return receipt, errors.Join(ErrInvalid, err)
		}
		artifacts := map[string]evolution.ArtifactRef{}
		for _, entry := range patch.Entries {
			if entry.Artifact != nil {
				artifacts[entry.Path] = *entry.Artifact
			}
		}
		for _, entry := range tree {
			if entry.Kind != "file" {
				continue
			}
			entry := entry
			if artifact, ok := artifacts[entry.Path]; ok {
				if s.config.RunArtifacts == nil {
					return receipt, ErrAuthority
				}
				err = s.materialize(ctx, entry, func(w io.Writer) error {
					if err := s.resultAuthority(ctx, scope); err != nil {
						return err
					}
					reader, err := s.config.RunArtifacts.ReadRunResultArtifact(ctx, scope, artifact)
					if err != nil {
						return err
					}
					defer reader.Close()
					_, err = io.Copy(w, io.LimitReader(reader, artifact.Size+1))
					return err
				})
			} else {
				err = s.materialize(ctx, entry, func(w io.Writer) error { return repo.ExportFile(ctx, scope.Base.CommitID, entry.Path, w) })
			}
			if err != nil {
				return receipt, err
			}
		}
		if err = s.validateProfileContent(descriptor); err != nil {
			return receipt, errors.Join(ErrInvalid, err)
		}
		entries := map[string]manifest.Entry{}
		for _, entry := range tree {
			entries[entry.Path] = entry
		}
		commit, err := repo.Commit(ctx, history.Snapshot{Profile: scope.Output.Profile, Entries: tree}, []string{scope.Base.CommitID}, "Generated result for run "+scope.RunID, func(path string) (io.ReadCloser, error) {
			return os.OpenFile(s.blobPath(entries[path].Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		})
		if err != nil {
			return receipt, err
		}
		receipt.BranchID = randomID()
		receipt.CommitID = commit
		receipt.State = "prepared"
	}
	// Check again after potentially slow input fetches, before the durable effect.
	if err = s.resultAuthority(ctx, scope); err != nil {
		return receipt, err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	if err = checkEvolutionOutput(ctx, tx, scope.GroupID, scope.BindingID, scope.Output); err != nil {
		return receipt, err
	}
	var branch any
	if !noChange {
		branch = receipt.BranchID
		if _, err = tx.ExecContext(ctx, "INSERT INTO branches(id,library_id,name,initial_base,issuer,subject,created_at) VALUES(?,?,?,?,?,?,?)", receipt.BranchID, scope.Output.LibraryID, "run-"+scope.RunID, scope.Base.CommitID, "evaluation-space", scope.RunID, s.now().Unix()); err != nil {
			return receipt, err
		}
		for _, entry := range tree {
			size, _ := strconv.ParseInt(entry.Size, 10, 64)
			var blob any
			if entry.Kind == "file" {
				blob = entry.Digest
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO branch_entries VALUES(?,?,?,?,?)", receipt.BranchID, entry.Path, entry.Kind, size, blob); err != nil {
				return receipt, err
			}
		}
	}
	scopeRaw, _ := json.Marshal(scope)
	patchRaw, _ := json.Marshal(patch)
	if _, err = tx.ExecContext(ctx, "INSERT INTO run_results(group_id,run_id,library_id,scope,patch,patch_digest,branch_id,commit_id,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", scope.GroupID, scope.RunID, scope.Output.LibraryID, scopeRaw, patchRaw, scope.PatchDigest, branch, receipt.CommitID, receipt.State, s.now().Unix()); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES('evaluation-space',?,'run.result',?,?,?,?)", scope.RunID, scope.Output.LibraryID, scope.RunID, scope.Fence, s.now().Unix()); err != nil {
		return receipt, err
	}
	if err = tx.Commit(); err != nil {
		return receipt, err
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("result_after_intent"); err != nil {
			s.poisoned = true
			return receipt, err
		}
	}
	if !noChange {
		if err = s.settleRunResult(ctx, scope.Output.LibraryID, receipt); err != nil {
			s.poisoned = true
			return receipt, err
		}
		receipt.State = "submitted"
	}
	return receipt, nil
}
func (s *Store) settleRunResult(ctx context.Context, library string, receipt evolution.RunResultReceipt) error {
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	coordinator, err := history.Coordinate(ctx, repo)
	if err != nil {
		return err
	}
	defer coordinator.Close()
	operation, err := coordinator.Update(ctx, "result-"+receipt.BranchID, "refs/heads/"+receipt.BranchID, "", receipt.CommitID)
	if err != nil {
		return err
	}
	if operation.State != "settled" {
		return ErrConflict
	}
	if s.branchCrash != nil {
		if err = s.branchCrash("result_after_ref"); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE branches SET state='ready' WHERE id=?", receipt.BranchID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_results SET state='submitted' WHERE branch_id=? AND state='prepared'", receipt.BranchID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) reconcileRunResults(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT library_id,run_id,patch_digest,branch_id,commit_id FROM run_results WHERE state='prepared' ORDER BY group_id,run_id")
	if err != nil {
		return err
	}
	type pending struct {
		library string
		receipt evolution.RunResultReceipt
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.library, &item.receipt.RunID, &item.receipt.PatchDigest, &item.receipt.BranchID, &item.receipt.CommitID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = s.settleRunResult(ctx, item.library, item.receipt); err != nil {
			return err
		}
	}
	return nil
}
