package authoring

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type EvolutionReservationOperation struct {
	OperationID   string                  `json:"operation_id"`
	Digest        string                  `json:"digest"`
	Configuration evolution.Configuration `json:"configuration"`
}

// ReserveEvolutionConfiguration owns all prefixes atomically. The immutable
// operation record makes retries lookupable without reconstructing partial work.
func (s *Store) ReserveEvolutionConfiguration(ctx context.Context, actor Actor, input EvolutionReservationOperation) (evolution.ActivationReceipt, error) {
	var result evolution.ActivationReceipt
	if input.Configuration.Validate() != nil || !manifest.ValidID(input.OperationID) {
		return result, ErrInvalid
	}
	body, err := json.Marshal(input.Configuration)
	if err != nil || len(body) > evolution.MaxDocumentBytes || evolution.Digest(body) != input.Digest {
		return result, ErrInvalid
	}
	for _, binding := range input.Configuration.Bindings {
		if err = s.authorizeLibrary(ctx, actor, binding.Output.LibraryID, "volume.configure"); err != nil {
			return result, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var digest, state string
	err = tx.QueryRowContext(ctx, "SELECT digest,state FROM evolution_reservation_operations WHERE operation_id=?", input.OperationID).Scan(&digest, &state)
	if err == nil {
		if digest != input.Digest {
			return result, ErrConflict
		}
		return evolution.ActivationReceipt{Participant: "studio", OperationID: input.OperationID, ConfigurationDigest: digest, Generation: input.Configuration.Revision, State: state}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	for _, binding := range input.Configuration.Bindings {
		var profile string
		if err = tx.QueryRowContext(ctx, "SELECT profile FROM libraries WHERE id=?", binding.Output.LibraryID).Scan(&profile); err != nil {
			return result, normalizeDB(err)
		}
		if profile != binding.Output.Profile {
			return result, ErrConflict
		}
		r := PrefixReservation{LibraryID: binding.Output.LibraryID, Prefix: binding.Output.Prefix, GroupID: input.Configuration.GroupID, BindingID: binding.ID, Generation: binding.Output.ReservationGeneration, OperationID: input.OperationID}
		if _, err = s.reserveEvolutionOutput(ctx, tx, actor, r); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_reservation_operations(operation_id,group_id,revision,digest,body,state) VALUES(?,?,?,?,?,'reserved')", input.OperationID, input.Configuration.GroupID, input.Configuration.Revision, input.Digest, string(body)); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return evolution.ActivationReceipt{Participant: "studio", OperationID: input.OperationID, ConfigurationDigest: input.Digest, Generation: input.Configuration.Revision, State: "reserved"}, nil
}

func (s *Store) EvolutionReservationOperation(ctx context.Context, actor Actor, operation, digest string) (evolution.ActivationReceipt, error) {
	var result evolution.ActivationReceipt
	if !manifest.ValidID(operation) || !manifest.ValidDigest(digest) {
		return result, ErrInvalid
	}
	var body, stored, state, group string
	var revision int64
	err := s.db.QueryRowContext(ctx, "SELECT body,digest,revision,state,group_id FROM evolution_reservation_operations WHERE operation_id=?", operation).Scan(&body, &stored, &revision, &state, &group)
	if err != nil {
		return result, normalizeDB(err)
	}
	var config evolution.Configuration
	if json.Unmarshal([]byte(body), &config) != nil || evolution.Digest([]byte(body)) != stored || config.GroupID != group || config.Revision != revision || config.Validate() != nil {
		return result, ErrInvalid
	}
	for _, binding := range config.Bindings {
		if err = s.authorizeLibrary(ctx, actor, binding.Output.LibraryID, "volume.configure"); err != nil {
			return result, err
		}
	}
	if stored != digest {
		return result, ErrConflict
	}
	return evolution.ActivationReceipt{Participant: "studio", OperationID: operation, ConfigurationDigest: stored, Generation: revision, State: state}, nil
}

func (s *Store) AdoptEvolutionConfiguration(ctx context.Context, actor Actor, operation, digest string) (evolution.ActivationReceipt, error) {
	return s.transitionEvolutionReservation(ctx, actor, operation, digest, "adopted")
}
func (s *Store) CancelEvolutionReservation(ctx context.Context, actor Actor, operation, digest string) (evolution.ActivationReceipt, error) {
	return s.transitionEvolutionReservation(ctx, actor, operation, digest, "released")
}

// Transition every output and the operation receipt in one transaction. A
// cancellation can release pending work only, never already adopted ownership.
func (s *Store) transitionEvolutionReservation(ctx context.Context, actor Actor, operation, digest, target string) (evolution.ActivationReceipt, error) {
	result, err := s.EvolutionReservationOperation(ctx, actor, operation, digest)
	if err != nil {
		return result, err
	}
	var body string
	if err = s.db.QueryRowContext(ctx, "SELECT body FROM evolution_reservation_operations WHERE operation_id=?", operation).Scan(&body); err != nil {
		return result, normalizeDB(err)
	}
	var config evolution.Configuration
	if json.Unmarshal([]byte(body), &config) != nil || evolution.Digest([]byte(body)) != digest {
		return result, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return result, ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM evolution_reservation_operations WHERE operation_id=? AND digest=?", operation, digest).Scan(&state); err != nil {
		return result, normalizeDB(err)
	}
	if state == target {
		result.State = target
		return result, nil
	}
	if state != "reserved" {
		return result, ErrConflict
	}
	prefixState := "released"
	if target == "adopted" {
		prefixState = "active"
	}
	for _, binding := range config.Bindings {
		if target == "adopted" {
			if err = writableReservationLibrary(ctx, tx, binding.Output.LibraryID); err != nil {
				return result, err
			}
		}
		candidate, err := transitionEvolutionCandidate(ctx, tx, PrefixReservation{LibraryID: binding.Output.LibraryID, Prefix: binding.Output.Prefix, GroupID: config.GroupID, BindingID: binding.ID, Generation: binding.Output.ReservationGeneration, OperationID: operation}, target == "adopted")
		if err != nil {
			return result, err
		}
		if !candidate {
			changed, err := tx.ExecContext(ctx, `UPDATE evolution_prefix_reservations SET state=? WHERE operation_id=? AND library_id=? AND prefix=? AND owner_group_id=? AND binding_id=? AND generation=? AND state='pending'`, prefixState, operation, binding.Output.LibraryID, binding.Output.Prefix, config.GroupID, binding.ID, binding.Output.ReservationGeneration)
			if err != nil {
				return result, err
			}
			count, err := changed.RowsAffected()
			if err != nil {
				return result, err
			}
			if count != 1 {
				return result, ErrConflict
			}
		}
		if err = s.reservationAudit(ctx, tx, actor, "evolution.configuration."+target, binding.Output.LibraryID, operation, binding.Output.ReservationGeneration); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE evolution_reservation_operations SET state=? WHERE operation_id=?", target, operation); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.State = target
	return result, nil
}

// ScopedEvolutionReservation checks caller scope against immutable stored identity.
func (s *Store) ScopedEvolutionReservation(ctx context.Context, actor Actor, group, operation, digest string, revision int64) (evolution.ActivationReceipt, error) {
	receipt, err := s.EvolutionReservationOperation(ctx, actor, operation, digest)
	if err != nil {
		return receipt, err
	}
	var storedGroup string
	if err = s.db.QueryRowContext(ctx, "SELECT group_id FROM evolution_reservation_operations WHERE operation_id=?", operation).Scan(&storedGroup); err != nil {
		return evolution.ActivationReceipt{}, normalizeDB(err)
	}
	if storedGroup != group || receipt.Generation != revision {
		return evolution.ActivationReceipt{}, ErrConflict
	}
	return receipt, nil
}
