package authoring

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
func (s *Store) ReserveEvolutionPrefix(ctx context.Context, actor Actor, r PrefixReservation, expectedOperation string) (PrefixReservation, error) {
	if !manifest.ValidID(r.LibraryID) || !manifest.ValidID(r.GroupID) || !manifest.ValidID(r.BindingID) || !manifest.ValidID(r.OperationID) || manifest.ValidPath(r.Prefix, true) != nil || r.Generation < 1 || expectedOperation != r.OperationID {
		return r, ErrInvalid
	}
	return s.reservationResult(ctx, actor, r.LibraryID, func(tx *sql.Tx, actor Actor) (PrefixReservation, error) {
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
		if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_prefix_reservations VALUES(?,?,?,?,?,'pending',?,?)", r.LibraryID, r.Prefix, r.GroupID, r.BindingID, r.Generation, r.OperationID, s.now().Unix()); err != nil {
			return r, err
		}
		r.State = "pending"
		return r, s.reservationAudit(ctx, tx, actor, "evolution.prefix.reserve", r.LibraryID, r.OperationID, r.Generation)
	})
}
func (s *Store) ActivateEvolutionPrefix(ctx context.Context, actor Actor, operation, library, prefix string, expectedGeneration int64) (PrefixReservation, error) {
	var r PrefixReservation
	err := s.reservationWrite(ctx, actor, library, func(tx *sql.Tx) error {
		var err error
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
		return s.reservationAudit(ctx, tx, actor, "evolution.prefix.activate", library, operation, r.Generation)
	})
	return r, err
}
func (s *Store) ReleaseEvolutionPrefix(ctx context.Context, actor Actor, operation, library, prefix string) error {
	return s.reservationWrite(ctx, actor, library, func(tx *sql.Tx) error {
		var err error
		var state string
		err = tx.QueryRowContext(ctx, "SELECT state FROM evolution_prefix_reservations WHERE operation_id=? AND library_id=? AND prefix=?", operation, library, prefix).Scan(&state)
		if err != nil {
			return err
		}
		if state == "released" {
			return nil
		}
		if state != "pending" {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_prefix_reservations SET state='released' WHERE operation_id=? AND library_id=? AND prefix=?", operation, library, prefix); err != nil {
			return err
		}
		return s.reservationAudit(ctx, tx, actor, "evolution.prefix.release", library, operation, 0)
	})
}

func (s *Store) reservationWrite(ctx context.Context, actor Actor, library string, fn func(*sql.Tx) error) error {
	if err := s.authorizeLibrary(ctx, actor, library, "volume.configure"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) reservationResult(ctx context.Context, actor Actor, library string, fn func(*sql.Tx, Actor) (PrefixReservation, error)) (PrefixReservation, error) {
	var result PrefixReservation
	err := s.reservationWrite(ctx, actor, library, func(tx *sql.Tx) error { var err error; result, err = fn(tx, actor); return err })
	return result, err
}
func (s *Store) reservationAudit(ctx context.Context, tx *sql.Tx, actor Actor, action, library, operation string, generation int64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit(issuer,subject,action,library_id,request_id,revision,created_at) VALUES(?,?,?,?,?,?,?)", actor.Issuer, actor.Subject, action, library, operation, generation, s.now().Unix())
	return err
}
