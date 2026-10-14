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
		if _, err = s.reserveEvolutionPrefix(ctx, tx, actor, r); err != nil {
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
	var body, stored, state string
	var revision int64
	err := s.db.QueryRowContext(ctx, "SELECT body,digest,revision,state FROM evolution_reservation_operations WHERE operation_id=?", operation).Scan(&body, &stored, &revision, &state)
	if err != nil {
		return result, normalizeDB(err)
	}
	var config evolution.Configuration
	if json.Unmarshal([]byte(body), &config) != nil || evolution.Digest([]byte(body)) != stored {
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
