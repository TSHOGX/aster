package projections

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type ServiceStateInput struct {
	RequestID        string `json:"request_id"`
	ServiceID        string `json:"service_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Enabled          bool   `json:"enabled"`
}

// SetServiceState changes admission state independently of immutable definitions.
// The caller must enforce platform service-management authority.
func (s *Store) SetServiceState(ctx context.Context, input ServiceStateInput) (int64, error) {
	if !manifest.ValidID(input.RequestID) || !manifest.ValidID(input.ServiceID) || input.ExpectedRevision < 1 {
		return 0, fmt.Errorf("invalid service state request")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var prior string
	var result int64
	err = tx.QueryRowContext(ctx, "SELECT request,revision FROM service_state_receipts WHERE request_id=?", input.RequestID).Scan(&prior, &result)
	if err == nil {
		if prior != string(raw) {
			return 0, ErrConflict
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	result = input.ExpectedRevision + 1
	update, err := tx.ExecContext(ctx, "UPDATE service_states SET enabled=?,revision=? WHERE service_id=? AND revision=?", input.Enabled, result, input.ServiceID, input.ExpectedRevision)
	if err != nil {
		return 0, err
	}
	count, err := update.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO service_state_receipts(request_id,request,revision) VALUES(?,?,?)", input.RequestID, string(raw), result); err != nil {
		return 0, err
	}
	return result, tx.Commit()
}
