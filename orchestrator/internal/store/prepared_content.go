package store

import "context"

// PreparedContentReferenced preserves content for every unfinished consumer,
// including a guest write whose result is known but final maintenance pause is
// still unresolved. Callers must hold the prepared store's exclusive lock while
// checking and unlinking; writers hold its shared lock until mutation commit.
func (s *Store) PreparedContentReferenced(ctx context.Context, owner, digest string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_guest_mutations g JOIN workspace_mutations m ON m.operation_id=g.operation_id LEFT JOIN workspace_write_maintenance w ON w.mutation_id=m.operation_id WHERE g.content_digest=? AND (m.state IN ('active','uncertain') OR (w.stage IS NOT NULL AND w.stage!='complete'))`, digest).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, tx.Commit()
}
