package authoring

import (
	"context"
	"database/sql"
	"errors"
)

// An active generation remains authoritative until its candidate is adopted.
func (s *Store) reserveEvolutionOutput(ctx context.Context, tx *sql.Tx, actor Actor, r PrefixReservation) (PrefixReservation, error) {
	if err := sameEvolutionOutput(ctx, tx, r); err != nil {
		return r, err
	}
	var prior PrefixReservation
	err := tx.QueryRowContext(ctx, "SELECT owner_group_id,binding_id,generation,state,operation_id FROM evolution_prefix_reservations WHERE library_id=? AND prefix=?", r.LibraryID, r.Prefix).Scan(&prior.GroupID, &prior.BindingID, &prior.Generation, &prior.State, &prior.OperationID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && prior.State != "active" {
		return s.reserveEvolutionPrefix(ctx, tx, actor, r)
	}
	if err != nil {
		return r, err
	}
	if prior.GroupID != r.GroupID || prior.BindingID != r.BindingID || r.Generation != prior.Generation+1 || prior.OperationID == r.OperationID {
		return r, ErrConflict
	}
	if err = writableReservationLibrary(ctx, tx, r.LibraryID); err != nil {
		return r, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_reservation_candidates WHERE library_id=? AND prefix=?", r.LibraryID, r.Prefix).Scan(&pending); err != nil {
		return r, err
	}
	if pending != 0 {
		return r, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_reservation_candidates(library_id,prefix,operation_id,previous_operation_id,generation) VALUES(?,?,?,?,?)", r.LibraryID, r.Prefix, r.OperationID, prior.OperationID, r.Generation); err != nil {
		return r, err
	}
	r.State = "pending"
	return r, s.reservationAudit(ctx, tx, actor, "evolution.prefix.candidate", r.LibraryID, r.OperationID, r.Generation)
}

func transitionEvolutionCandidate(ctx context.Context, tx *sql.Tx, r PrefixReservation, adopt bool) (bool, error) {
	var prior string
	var generation int64
	err := tx.QueryRowContext(ctx, "SELECT previous_operation_id,generation FROM evolution_reservation_candidates WHERE library_id=? AND prefix=? AND operation_id=?", r.LibraryID, r.Prefix, r.OperationID).Scan(&prior, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if generation != r.Generation {
		return true, ErrConflict
	}
	if adopt {
		changed, err := tx.ExecContext(ctx, `UPDATE evolution_prefix_reservations SET operation_id=?,generation=? WHERE library_id=? AND prefix=? AND operation_id=? AND generation=? AND owner_group_id=? AND binding_id=? AND state='active'`, r.OperationID, r.Generation, r.LibraryID, r.Prefix, prior, r.Generation-1, r.GroupID, r.BindingID)
		if err != nil {
			return true, err
		}
		count, err := changed.RowsAffected()
		if err != nil {
			return true, err
		}
		if count != 1 {
			return true, ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM evolution_reservation_candidates WHERE library_id=? AND prefix=? AND operation_id=?", r.LibraryID, r.Prefix, r.OperationID)
	return true, err
}

// Ordinary activation cannot transfer a binding's retained output ownership.
// Transfers require their own impact/disposition operation, not another reserve.
func sameEvolutionOutput(ctx context.Context, tx *sql.Tx, r PrefixReservation) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM evolution_prefix_reservations WHERE owner_group_id=? AND binding_id=? AND state IN ('pending','active') AND (library_id<>? OR prefix<>?)`, r.GroupID, r.BindingID, r.LibraryID, r.Prefix).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}
