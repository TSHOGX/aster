package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type WorkflowDraftInput struct {
	ID               string             `json:"id"`
	ExpectedRevision int64              `json:"expected_revision"`
	Workflow         evolution.Workflow `json:"workflow"`
}
type WorkflowDraft struct {
	ID       string             `json:"id"`
	Revision int64              `json:"revision"`
	Workflow evolution.Workflow `json:"workflow"`
}

// SaveWorkflowDraft stores editable graph content separately from executable
// catalog revisions. Layout-only metadata may be layered later without changing
// Workflow.Canonical identity.
func (s *Store) SaveWorkflowDraft(ctx context.Context, input WorkflowDraftInput) (WorkflowDraft, error) {
	var result WorkflowDraft
	if !manifest.ValidID(input.ID) {
		return result, fmt.Errorf("invalid workflow identity")
	}
	if input.ExpectedRevision < 0 || input.Workflow.ID != input.ID || input.Workflow.Validate() != nil {
		return result, fmt.Errorf("invalid workflow draft")
	}
	raw, err := input.Workflow.Canonical()
	if err != nil {
		return result, fmt.Errorf("invalid workflow identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var revision int64
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT revision,body FROM workflow_drafts WHERE id=?", input.ID).Scan(&revision, &prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		revision = 0
	}
	if revision != input.ExpectedRevision {
		return result, ErrConflict
	}
	result = WorkflowDraft{ID: input.ID, Revision: revision + 1, Workflow: input.Workflow}
	body := raw
	if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_drafts(id,revision,body) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", input.ID, result.Revision, body); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) WorkflowDraft(ctx context.Context, id string) (WorkflowDraft, error) {
	var result WorkflowDraft
	var raw []byte
	if !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid workflow identity")
	}
	if err := s.db.QueryRowContext(ctx, "SELECT revision,body FROM workflow_drafts WHERE id=?", id).Scan(&result.Revision, &raw); err != nil {
		return result, err
	}
	result.ID = id
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	result.Workflow = workflow
	return result, nil
}
