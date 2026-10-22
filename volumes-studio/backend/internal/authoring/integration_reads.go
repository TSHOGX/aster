package authoring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type IntegrationView struct {
	Evaluations      []EvaluationSummary        `json:"evaluations"`
	Evaluation       string                     `json:"evaluation"`
	Acceptance       string                     `json:"acceptance"`
	AcceptedCommit   string                     `json:"accepted_commit,omitempty"`
	MergeOperationID string                     `json:"merge_operation_id,omitempty"`
	Digest           string                     `json:"digest"`
	Identity         evolution.PreparedIdentity `json:"identity"`
	Readiness        string                     `json:"readiness"`
	ConflictCount    int                        `json:"conflict_count"`
	Reviews          []evolution.Review         `json:"reviews"`
}

func (s *Store) Integration(ctx context.Context, a Actor, library, digest string) (IntegrationView, error) {
	var view IntegrationView
	if !manifest.ValidDigest(digest) {
		return view, ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return view, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT identity,conflicts FROM prepared_merges WHERE library_id=? AND digest=?", library, digest).Scan(&raw, &view.ConflictCount); err != nil {
		return view, normalizeDB(err)
	}
	if err := json.Unmarshal(raw, &view.Identity); err != nil {
		return view, err
	}
	computed, err := view.Identity.ApprovalDigest()
	if err != nil || computed != digest {
		return view, ErrInvalid
	}
	view.Evaluation = "not_requested"
	if len(view.Identity.Checks) > 0 {
		view.Evaluation = "pending"
	}
	view.Digest = digest
	view.Readiness = "ready_for_review"
	view.Acceptance = "not_accepted"
	var operationState string
	err = s.db.QueryRowContext(ctx, "SELECT id,new_head,state FROM merge_operations WHERE library_id=? AND prepared_digest=?", library, digest).Scan(&view.MergeOperationID, &view.AcceptedCommit, &operationState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if err == nil {
		if operationState == "settled" {
			view.Acceptance = "accepted"
			view.Readiness = "historical"
		} else {
			view.Acceptance = "acceptance_pending"
			view.Readiness = "reconciliation_required"
		}
	}
	if view.Acceptance == "not_accepted" {
		if err = s.checkPrepared(ctx, library, digest, view.Identity, view.ConflictCount); err != nil {
			switch {
			case errors.Is(err, ErrConflict):
				view.Readiness = "stale_or_conflicted"
			case errors.Is(err, ErrBusy):
				view.Readiness = "checks_required"
			default:
				return view, err
			}
		}
	}
	view.Evaluations, err = s.evaluationSummaries(ctx, library, digest)
	if err != nil {
		return view, err
	}
	if len(view.Identity.Checks) > 0 {
		allPassed := true
		for _, check := range view.Identity.Checks {
			passed, err := s.evaluationCheckPassed(ctx, library, digest, check.Digest)
			if err != nil {
				return view, err
			}
			if !passed {
				allPassed = false
			}
		}
		if allPassed {
			view.Evaluation = "pass"
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,issuer,subject,verdict,rationale,created_at FROM merge_reviews WHERE prepared_digest=? ORDER BY rowid", digest)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	view.Reviews = []evolution.Review{}
	for rows.Next() {
		var review evolution.Review
		var created int64
		if err = rows.Scan(&review.ID, &review.Reviewer.Issuer, &review.Reviewer.Subject, &review.Verdict, &review.Rationale, &created); err != nil {
			return view, err
		}
		review.PreparedDigest = digest
		review.Reviewer.Kind = "human"
		review.CreatedAt = time.Unix(created, 0).UTC()
		view.Reviews = append(view.Reviews, review)
	}
	return view, rows.Err()
}

type IntegrationChanges struct {
	Digest  string   `json:"digest"`
	Changes []Change `json:"changes"`
	Next    string   `json:"next"`
}

func (s *Store) IntegrationChanges(ctx context.Context, a Actor, library, digest, after string) (IntegrationChanges, error) {
	view, err := s.Integration(ctx, a, library, digest)
	if err != nil {
		return IntegrationChanges{}, err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return IntegrationChanges{}, err
	}
	base, err := repo.Snapshot(ctx, view.Identity.Main.CommitID)
	if err != nil {
		return IntegrationChanges{}, err
	}
	result, err := repo.Snapshot(ctx, view.Identity.Result.CommitID)
	if err != nil {
		return IntegrationChanges{}, err
	}
	page := IntegrationChanges{Digest: digest, Changes: []Change{}}
	for _, change := range compareEntries(base.Entries, result.Entries) {
		if change.Path <= after {
			continue
		}
		if len(page.Changes) == 200 {
			page.Next = page.Changes[199].Path
			break
		}
		page.Changes = append(page.Changes, change)
	}
	return page, nil
}

type IntegrationFile struct {
	CommitID string         `json:"commit_id"`
	Entry    manifest.Entry `json:"entry"`
	Text     *string        `json:"text,omitempty"`
	Preview  string         `json:"preview"`
}

func (s *Store) IntegrationFile(ctx context.Context, a Actor, library, digest, side, path string) (IntegrationFile, error) {
	var file IntegrationFile
	if manifest.ValidPath(path, false) != nil || (side != "main" && side != "result") {
		return file, ErrInvalid
	}
	view, err := s.Integration(ctx, a, library, digest)
	if err != nil {
		return file, err
	}
	ref := view.Identity.Result
	if side == "main" {
		ref = view.Identity.Main
	}
	return s.CommitFile(ctx, a, library, ref.CommitID, path)
}
