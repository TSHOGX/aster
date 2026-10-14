package product

import (
	"context"
	"database/sql"
	"errors"
	"harness-platform/orchestrator/internal/evaluationclient"
)

// EvolutionWorker resumes durable pending projections without browser sessions.
// One operation per step and a rotating cursor prevent an offline group from
// starving unrelated groups. The participant transport handles uncertain replies.
type EvolutionWorker struct {
	Store      *Store
	Evaluation *evaluationclient.Client
	cursor     string
}

func (w *EvolutionWorker) Step(ctx context.Context) error {
	if w.Store == nil || w.Evaluation == nil {
		return nil
	}
	var group, operation string
	query := `SELECT c.group_id,c.operation_id FROM evolution_group_configs c WHERE c.state='activating' AND c.group_id>? AND NOT EXISTS(SELECT 1 FROM evolution_activation_receipts r WHERE r.operation_id=c.operation_id AND r.participant='evaluation-space' AND r.state='accepted' AND r.config_digest=c.desired_digest) ORDER BY c.group_id LIMIT 1`
	err := w.Store.reader.QueryRowContext(ctx, query, w.cursor).Scan(&group, &operation)
	if errors.Is(err, sql.ErrNoRows) {
		w.cursor = ""
		return nil
	}
	if err != nil {
		return err
	}
	w.cursor = group
	projection, err := readEvolutionProjection(ctx, w.Store.reader, operation)
	if err != nil {
		return err
	}
	allowed, err := w.Store.AuthorizeEvolutionProjection(ctx, ProjectionScope{GroupID: group, OperationID: operation, Revision: projection.Configuration.Revision, Digest: projection.Digest})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	receipt, err := w.Evaluation.Reconcile(ctx, projection)
	if err != nil {
		return err
	}
	return w.Store.write(ctx, func(tx *sql.Tx) error { return w.Store.acceptEvolutionReceipt(ctx, tx, "service:evolution", receipt) })
}
