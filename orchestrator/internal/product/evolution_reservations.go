package product

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"errors"
	"strings"
)

type PrefixReservation struct {
	LibraryID   string `json:"library_id"`
	Prefix      string `json:"prefix"`
	GroupID     string `json:"group_id"`
	BindingID   string `json:"binding_id"`
	Generation  int64  `json:"generation"`
	State       string `json:"state"`
	OperationID string `json:"operation_id"`
}

func prefixOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func (s *Store) ReserveEvolutionPrefix(ctx context.Context, token string, r PrefixReservation, expectedOperation string) (PrefixReservation, error) {
	if !validID(r.LibraryID) || !validID(r.GroupID) || !validID(r.BindingID) || !validID(r.OperationID) || manifest.ValidPath(r.Prefix, true) != nil || r.Generation < 1 || expectedOperation != r.OperationID {
		return r, ErrInvalid
	}
	return s.writeResult(ctx, token, func(tx *sql.Tx, actor Account) (PrefixReservation, error) {
		var prior PrefixReservation
		err := tx.QueryRowContext(ctx, "SELECT library_id,prefix,owner_group_id,binding_id,generation,state,operation_id FROM evolution_prefix_reservations WHERE library_id=? AND prefix=?", r.LibraryID, r.Prefix).Scan(&prior.LibraryID, &prior.Prefix, &prior.GroupID, &prior.BindingID, &prior.Generation, &prior.State, &prior.OperationID)
		if err == nil {
			if prior.GroupID == r.GroupID && prior.BindingID == r.BindingID && prior.Generation == r.Generation && prior.OperationID == r.OperationID {
				return prior, nil
			}
			return r, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT prefix,owner_group_id FROM evolution_prefix_reservations WHERE library_id=? AND state IN ('pending','active')", r.LibraryID)
		if err != nil {
			return r, err
		}
		for rows.Next() {
			var prefix, group string
			if err = rows.Scan(&prefix, &group); err != nil {
				rows.Close()
				return r, err
			}
			if group != r.GroupID || prefixOverlap(prefix, r.Prefix) {
				rows.Close()
				return r, ErrConflict
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return r, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_prefix_reservations VALUES(?,?,?,?,?,'pending',?,?)", r.LibraryID, r.Prefix, r.GroupID, r.BindingID, r.Generation, r.OperationID, s.timestamp()); err != nil {
			return r, err
		}
		r.State = "pending"
		return r, s.auditEvent(ctx, tx, actor.ID, "evolution.prefix.reserve", "volume", r.LibraryID, "", r.Generation, r)
	})
}
func (s *Store) ActivateEvolutionPrefix(ctx context.Context, token, operation, library, prefix string, expectedGeneration int64) (PrefixReservation, error) {
	var r PrefixReservation
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT library_id,prefix,owner_group_id,binding_id,generation,state,operation_id FROM evolution_prefix_reservations WHERE operation_id=? AND library_id=? AND prefix=?", operation, library, prefix).Scan(&r.LibraryID, &r.Prefix, &r.GroupID, &r.BindingID, &r.Generation, &r.State, &r.OperationID)
		if err != nil {
			return err
		}
		if r.Generation == expectedGeneration && r.State == "active" {
			return nil
		}
		if r.Generation != expectedGeneration || r.State != "pending" {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_prefix_reservations SET state='active' WHERE operation_id=? AND library_id=? AND prefix=?", operation, library, prefix); err != nil {
			return err
		}
		r.State = "active"
		return s.auditEvent(ctx, tx, actor.ID, "evolution.prefix.activate", "volume", library, operation, r.Generation, r)
	})
	return r, err
}
func (s *Store) ReleaseEvolutionPrefix(ctx context.Context, token, operation, library, prefix string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		var state string
		err = tx.QueryRowContext(ctx, "SELECT state FROM evolution_prefix_reservations WHERE operation_id=? AND library_id=? AND prefix=?", operation, library, prefix).Scan(&state)
		if err != nil {
			return err
		}
		if state == "released" {
			return nil
		}
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_prefix_reservations SET state='released' WHERE operation_id=? AND library_id=? AND prefix=?", operation, library, prefix); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.prefix.release", "volume", library, operation, 0, map[string]string{"prefix": prefix})
	})
}
func (s *Store) writeResult(ctx context.Context, token string, fn func(*sql.Tx, Account) (PrefixReservation, error)) (PrefixReservation, error) {
	var result PrefixReservation
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		result, err = fn(tx, actor)
		return err
	})
	return result, err
}
