package product

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"context"
	"encoding/json"
	"errors"
)

type ProjectionScope struct {
	GroupID     string `json:"group_id"`
	OperationID string `json:"operation_id"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
}

// AuthorizeEvolutionProjection is called only by the private authenticated
// identity boundary. Receipt adoption does not imply central activation success.
func (s *Store) AuthorizeEvolutionProjection(ctx context.Context, scope ProjectionScope) (bool, error) {
	return authorizeEvolutionProjection(ctx, s.reader, scope)
}
func authorizeEvolutionProjection(ctx context.Context, q queryer, scope ProjectionScope) (bool, error) {
	if !validID(scope.GroupID) || !validID(scope.OperationID) || scope.Revision < 1 || !manifest.ValidDigest(scope.Digest) {
		return false, ErrInvalid
	}
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM evolution_group_configs c JOIN account_groups g ON g.id=c.group_id WHERE c.group_id=? AND c.operation_id=? AND c.desired_revision=? AND c.desired_digest=? AND c.state IN ('activating','active')`, scope.GroupID, scope.OperationID, scope.Revision, scope.Digest).Scan(&count)
	if err != nil || count != 1 {
		return false, err
	}
	var raw string
	if err = q.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE operation_id=?", scope.OperationID).Scan(&raw); err != nil {
		return false, err
	}
	var config evolution.Configuration
	if err = json.Unmarshal([]byte(raw), &config); err != nil {
		return false, err
	}
	if err = authorizeEvolutionSources(ctx, q, config); err != nil {
		if errors.Is(err, ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
