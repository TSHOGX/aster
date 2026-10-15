package authoring

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
)

// EvolutionBases resolves each output Volume once for the exact adopted operation.
// Content read authority is checked in addition to configuration authority.
func (s *Store) EvolutionBases(ctx context.Context, actor Actor, group, operation, digest string, revision int64) (map[string]evolution.ContentRef, error) {
	receipt, err := s.ScopedEvolutionReservation(ctx, actor, group, operation, digest, revision)
	if err != nil {
		return nil, err
	}
	if receipt.State != "adopted" {
		return nil, ErrConflict
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT body FROM evolution_reservation_operations WHERE operation_id=?", operation).Scan(&raw); err != nil {
		return nil, normalizeDB(err)
	}
	var config evolution.Configuration
	if err = json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	bases := map[string]evolution.ContentRef{}
	for _, b := range config.Bindings {
		if !b.Enabled {
			continue
		}
		if _, ok := bases[b.Output.LibraryID]; ok {
			continue
		}
		ref, err := s.MainCommit(ctx, actor, b.Output.LibraryID)
		if err != nil {
			return nil, err
		}
		if ref.Profile != b.Output.Profile {
			return nil, ErrConflict
		}
		bases[ref.LibraryID] = ref
	}
	return bases, nil
}
