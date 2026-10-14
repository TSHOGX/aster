package product

import (
	"context"
	"database/sql"
	"errors"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/studioclient"
)

// EvolutionWorker resumes durable pending projections without browser sessions.
// One operation per step and a rotating cursor prevent an offline group from
// starving unrelated groups. The participant transport handles uncertain replies.
type EvolutionWorker struct {
	Store         *Store
	Evaluation    *evaluationclient.Client
	Studio        *studioclient.Client
	cursor        string
	discardCursor string
}

func (w *EvolutionWorker) Step(ctx context.Context) error {
	if w.Store == nil || w.Evaluation == nil {
		return nil
	}
	if w.Studio != nil {
		handled, err := w.discard(ctx)
		if handled || err != nil {
			return err
		}
	}
	var group, operation string
	query := `SELECT c.group_id,c.operation_id FROM evolution_group_configs c WHERE c.state='activating' AND c.group_id>? AND (? OR NOT EXISTS(SELECT 1 FROM evolution_activation_receipts r WHERE r.operation_id=c.operation_id AND r.participant='evaluation-space' AND r.state='accepted' AND r.config_digest=c.desired_digest)) ORDER BY c.group_id LIMIT 1`
	err := w.Store.reader.QueryRowContext(ctx, query, w.cursor, w.Studio != nil).Scan(&group, &operation)
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
	var studioProjection studioclient.Projection
	if w.Studio != nil {
		studioProjection = studioclient.Projection{OperationID: projection.OperationID, Digest: projection.Digest, Configuration: projection.Configuration}
		if _, err = w.Studio.Reserve(ctx, studioProjection); err != nil {
			return err
		}
	}
	receipt, err := w.Evaluation.Reconcile(ctx, projection)
	if errors.Is(err, evaluationclient.ErrRejected) {
		return w.Store.write(ctx, func(tx *sql.Tx) error {
			return w.Store.failEvolutionParticipant(ctx, tx, "service:evolution", operation, "evaluation-space", projection.Digest, "catalog_rejected")
		})
	}
	if err != nil {
		return err
	}
	if err = w.Store.write(ctx, func(tx *sql.Tx) error { return w.Store.acceptEvolutionReceipt(ctx, tx, "service:evolution", receipt) }); err != nil {
		return err
	}
	if w.Studio == nil {
		return nil
	}
	receipt, err = w.Studio.Adopt(ctx, studioProjection)
	if err != nil {
		return err
	}
	return w.Store.write(ctx, func(tx *sql.Tx) error {
		if err := w.Store.acceptEvolutionReceipt(ctx, tx, "service:evolution", receipt); err != nil {
			return err
		}
		_, err := w.Store.settleEvolutionActivation(ctx, tx, "service:evolution", operation)
		return err
	})
}

func (w *EvolutionWorker) discard(ctx context.Context) (bool, error) {
	var operation, group string
	err := w.Store.reader.QueryRowContext(ctx, "SELECT operation_id,group_id FROM evolution_group_configs WHERE state='discarding' AND group_id>? ORDER BY group_id LIMIT 1", w.discardCursor).Scan(&operation, &group)
	if errors.Is(err, sql.ErrNoRows) {
		w.discardCursor = ""
		return false, nil
	}
	if err != nil {
		return false, err
	}
	w.discardCursor = group
	projection, err := readEvolutionProjection(ctx, w.Store.reader, operation)
	if err != nil {
		return true, err
	}
	p := studioclient.Projection{OperationID: operation, Digest: projection.Digest, Configuration: projection.Configuration}
	receipt, err := w.Studio.Lookup(ctx, p)
	if err != nil && !errors.Is(err, studioclient.ErrNotFound) {
		return true, err
	}
	if err == nil {
		if receipt.State == "adopted" {
			return true, ErrConflict
		}
		if receipt.State != "released" {
			if _, err = w.Studio.Cancel(ctx, p); err != nil {
				return true, err
			}
		}
	}
	return true, w.Store.write(ctx, func(tx *sql.Tx) error {
		changed, err := tx.ExecContext(ctx, "UPDATE evolution_group_configs SET state='discarded' WHERE operation_id=? AND desired_digest=? AND state='discarding'", operation, projection.Digest)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return w.Store.auditEvent(ctx, tx, "service:evolution", "evolution.activation.discarded", "group", projection.Configuration.GroupID, operation, projection.Configuration.Revision, nil)
	})
}
