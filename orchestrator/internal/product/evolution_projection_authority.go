package product

import (
	manifest "aster/contracts/volumemanifest"
	"context"
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
	if !validID(scope.GroupID) || !validID(scope.OperationID) || scope.Revision < 1 || !manifest.ValidDigest(scope.Digest) {
		return false, ErrInvalid
	}
	var count int
	err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM evolution_group_configs c JOIN account_groups g ON g.id=c.group_id WHERE c.group_id=? AND c.operation_id=? AND c.desired_revision=? AND c.desired_digest=? AND c.state IN ('activating','active')`, scope.GroupID, scope.OperationID, scope.Revision, scope.Digest).Scan(&count)
	return count == 1, err
}
