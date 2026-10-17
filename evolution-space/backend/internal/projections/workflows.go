package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type workflowResolver struct{ tx *sql.Tx }

func (r workflowResolver) ResolveWorkflow(ctx context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	if ref.Validate() != nil {
		return evolution.Workflow{}, fmt.Errorf("invalid workflow reference")
	}
	var raw []byte
	var digest string
	if err := r.tx.QueryRowContext(ctx, "SELECT digest,body FROM workflow_revisions WHERE id=? AND revision=?", ref.ID, ref.Revision).Scan(&digest, &raw); err != nil {
		return evolution.Workflow{}, err
	}
	if digest != ref.Digest || evolution.Digest(raw) != digest {
		return evolution.Workflow{}, fmt.Errorf("workflow revision integrity mismatch")
	}
	return evolution.DecodeWorkflow(bytes.NewReader(raw))
}

// PublishWorkflow is the storage boundary for Evolution Space's immutable
// workflow catalog. Callers must authorize publication before invoking it.
func (s *Store) PublishWorkflow(ctx context.Context, ref evolution.Revision, body []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = publishWorkflow(ctx, tx, ref, body); err != nil {
		return err
	}
	return tx.Commit()
}
func publishWorkflow(ctx context.Context, tx *sql.Tx, ref evolution.Revision, body []byte) error {
	if ref.Validate() != nil || len(body) > evolution.MaxDocumentBytes || evolution.Digest(body) != ref.Digest {
		return fmt.Errorf("invalid workflow revision")
	}
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(body))
	if err != nil || workflow.ID != ref.ID {
		return fmt.Errorf("invalid workflow document")
	}
	if _, err = compileParameters(workflow.ParametersSchema); err != nil {
		return err
	}
	canonical, err := workflow.Canonical()
	if err != nil || !bytes.Equal(canonical, body) {
		return fmt.Errorf("canonical workflow document required")
	}
	var priorDigest string
	err = tx.QueryRowContext(ctx, "SELECT digest FROM workflow_revisions WHERE id=? AND revision=?", ref.ID, ref.Revision).Scan(&priorDigest)
	if err == nil {
		if priorDigest != ref.Digest {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0) FROM workflow_revisions WHERE id=?", ref.ID).Scan(&revision); err != nil {
		return err
	}
	if ref.Revision != revision+1 {
		return ErrConflict
	}
	if err = evolution.ValidateClosure(ctx, workflow, workflowResolver{tx}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO workflow_revisions(id,revision,digest,body) VALUES(?,?,?,?)", ref.ID, ref.Revision, ref.Digest, body); err != nil {
		return err
	}
	return nil
}
func (s *Store) Workflow(ctx context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return evolution.Workflow{}, err
	}
	defer tx.Rollback()
	return (workflowResolver{tx}).ResolveWorkflow(ctx, ref)
}
