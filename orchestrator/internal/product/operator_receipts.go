package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func readOperatorReceipt(ctx context.Context, q queryer, request adminRequest) (json.RawMessage, bool, error) {
	var action, target, digest, raw string
	err := q.QueryRowContext(ctx, `SELECT action,target_id,body_digest,result FROM operator_receipts WHERE request_id=?`, request.ID).Scan(&action, &target, &digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if action != request.Action || target != request.Target || digest != request.Digest {
		return nil, false, Admission("request_id_conflict", ErrConflict)
	}
	return json.RawMessage(raw), true, nil
}
func (s *Store) writeOperatorReceipt(ctx context.Context, tx *sql.Tx, request adminRequest, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operator_receipts(request_id,action,target_id,body_digest,result,created_at) VALUES(?,?,?,?,?,?)`, request.ID, request.Action, request.Target, request.Digest, string(raw), s.timestamp())
	return err
}
