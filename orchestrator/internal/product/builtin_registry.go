package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func registeredBuiltin(ctx context.Context, q queryer, reference ArtifactReference, newSelection bool) (BuiltinVolume, error) {
	var artifact BuiltinVolume
	var roles, entries, retired string
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,enabled,retired_at FROM builtin_volumes WHERE id=? AND version=?`, reference.ID, reference.Version).Scan(&artifact.Reference.ID, &artifact.Reference.Version, &artifact.Reference.Digest, &artifact.StoreID, &artifact.PublicationPurpose, &artifact.MinimumBytes, &roles, &artifact.CleanRuntime, &entries, &enabled, &retired)
	if errors.Is(err, sql.ErrNoRows) {
		return artifact, ErrArtifactUnavailable
	}
	if err != nil {
		return artifact, err
	}
	if artifact.Reference != reference || retired != "" || newSelection && !enabled {
		return artifact, ErrArtifactUnavailable
	}
	if err = json.Unmarshal([]byte(roles), &artifact.Roles); err != nil {
		return artifact, err
	}
	if err = json.Unmarshal([]byte(entries), &artifact.Entries); err != nil {
		return artifact, err
	}
	return artifact, nil
}
