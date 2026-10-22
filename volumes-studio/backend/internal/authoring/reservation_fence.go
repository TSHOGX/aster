package authoring

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
)

// CheckEvolutionOutput verifies current ownership, not a historical operation
// receipt. Pending candidates do not replace the active generation until adoption.
// Effectful callers must repeat this check inside their write transaction.
func (s *Store) CheckEvolutionOutput(ctx context.Context, actor Actor, group, binding string, output evolution.OutputBinding) error {
	if !manifest.ValidID(group) || !manifest.ValidID(binding) || output.Validate() != nil {
		return ErrInvalid
	}
	if err := s.authorizeLibrary(ctx, actor, output.LibraryID, "volume.configure"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return checkEvolutionOutput(ctx, tx, group, binding, output)
}
func checkEvolutionOutput(ctx context.Context, tx *sql.Tx, group, binding string, output evolution.OutputBinding) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM evolution_prefix_reservations r JOIN libraries l ON l.id=r.library_id WHERE r.library_id=? AND r.prefix=? AND r.owner_group_id=? AND r.binding_id=? AND r.generation=? AND r.state='active' AND l.archived=0 AND l.profile=? AND l.evolution_owner_group=?`, output.LibraryID, output.Prefix, group, binding, output.ReservationGeneration, output.Profile, group).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
