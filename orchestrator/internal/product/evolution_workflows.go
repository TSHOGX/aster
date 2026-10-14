package product

import (
	"bytes"
	"context"
	"database/sql"

	"aster/contracts/evolution"
)

type catalogWorkflowResolver struct{ tx *sql.Tx }

func (r catalogWorkflowResolver) ResolveWorkflow(ctx context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	var body, digest string
	var enabled, retired bool
	err := r.tx.QueryRowContext(ctx, "SELECT body,digest,enabled,retired FROM evolution_catalog_revisions WHERE kind='workflow' AND id=? AND revision=?", ref.ID, ref.Revision).Scan(&body, &digest, &enabled, &retired)
	if err != nil {
		return evolution.Workflow{}, err
	}
	if !enabled || retired || digest != ref.Digest || evolution.Digest([]byte(body)) != digest {
		return evolution.Workflow{}, ErrInvalid
	}
	return evolution.DecodeWorkflow(bytes.NewBufferString(body))
}
