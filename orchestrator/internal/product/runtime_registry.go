package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
)

// RegisterSandbox is a trusted operator port. It retains exact immutable
// metadata and never changes the selected database environment.
func (s *Store) RegisterSandbox(ctx context.Context, release RegisteredSandbox) error {
	if err := release.Reference.validate(); err != nil {
		return err
	}
	raw, err := runtimerelease.Bytes(release.Manifest)
	if err != nil {
		return err
	}
	if _, err = runtimerelease.Load(bytes.NewReader(raw), release.Reference.Digest); err != nil {
		return Admission("runtime_incompatible", ErrConflict)
	}
	if release.StoreID == "" || !validID(release.TemplateID) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		existing, err := registeredSandbox(ctx, tx, release.Reference, false)
		if err == nil {
			old, _ := runtimerelease.Bytes(existing.Manifest)
			if existing.StoreID != release.StoreID || existing.TemplateID != release.TemplateID || !bytes.Equal(old, raw) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrArtifactUnavailable) {
			return err
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM registered_sandboxes WHERE id=? AND version=?)`, release.Reference.ID, release.Reference.Version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
		state, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO registered_sandboxes(id,version,digest,store_id,template_id,manifest,created_at) VALUES(?,?,?,?,?,?,?)`, release.Reference.ID, release.Reference.Version, release.Reference.Digest, release.StoreID, release.TemplateID, string(raw), s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, "operator", "runtime.register", "sandbox_release", release.Reference.ID+"/"+release.Reference.Version, "", 1, map[string]any{"reference": release.Reference, "store_id": release.StoreID, "template_id": release.TemplateID})
	})
}
func ValidateBuiltinVolume(artifact BuiltinVolume) error {
	if err := artifact.Reference.validate(); err != nil {
		return err
	}
	if artifact.StoreID == "" || !content.ValidPurpose(artifact.PublicationPurpose) || artifact.MinimumBytes <= 0 || len(artifact.Roles) == 0 || len(artifact.Roles) > 35 || content.Digest(artifact.Entries) != artifact.Reference.Digest {
		return ErrInvalid
	}
	roles := slices.Clone(artifact.Roles)
	slices.Sort(roles)
	for index, role := range roles {
		if index > 0 && role == roles[index-1] {
			return ErrInvalid
		}
		if !validID(role) || len(role) > 64 {
			return ErrInvalid
		}
		if role == "shared_assets" && artifact.PublicationPurpose != "shared_assets" || role == "agent_config" && artifact.PublicationPurpose != "config" {
			return ErrInvalid
		}
		if role == "runtime" && !artifact.CleanRuntime {
			return ErrInvalid
		}
	}
	total, err := content.ValidateEntries(artifact.Entries, content.ProfileForPurpose(artifact.PublicationPurpose))
	if err != nil {
		return ErrInvalid
	}
	if total > artifact.MinimumBytes {
		return ErrInvalid
	}
	if artifact.PublicationPurpose == "authored" {
		if artifact.CleanRuntime {
			return ErrInvalid
		}
		for _, role := range artifact.Roles {
			if role != "data" && role != "workspace" {
				return ErrInvalid
			}
		}
	}
	if artifact.PublicationPurpose == "shared_assets" {
		if artifact.CleanRuntime || len(artifact.Roles) != 1 || artifact.Roles[0] != "shared_assets" {
			return ErrInvalid
		}
	}

	return nil
}
func checkBuiltinRegistration(ctx context.Context, q queryer, artifact BuiltinVolume) (bool, error) {
	roles := slices.Clone(artifact.Roles)
	slices.Sort(roles)
	var digest, store, purpose, oldRoles, retired string
	var minimum int64
	var clean bool
	err := q.QueryRowContext(ctx, `SELECT digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,retired_at FROM builtin_volumes WHERE id=? AND version=?`, artifact.Reference.ID, artifact.Reference.Version).Scan(&digest, &store, &purpose, &minimum, &oldRoles, &clean, &retired)
	if err == nil {
		if retired != "" {
			return true, Admission("artifact_retired", ErrConflict)
		}
		var declared []string
		if err = json.Unmarshal([]byte(oldRoles), &declared); err != nil {
			return true, err
		}
		slices.Sort(declared)
		if digest != artifact.Reference.Digest || store != artifact.StoreID || purpose != artifact.PublicationPurpose || minimum != artifact.MinimumBytes || clean != artifact.CleanRuntime || !slices.Equal(declared, roles) {
			return true, ErrConflict
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return true, err
	}
	return false, nil
}

// CheckBuiltinRegistration rejects incompatible declarations before publication
// can allocate storage. Registration repeats the check under the writer lock.
func (s *Store) CheckBuiltinRegistration(ctx context.Context, artifact BuiltinVolume) error {
	if err := ValidateBuiltinVolume(artifact); err != nil {
		return err
	}
	_, err := checkBuiltinRegistration(ctx, s.reader, artifact)
	return err
}
func (s *Store) RegisterBuiltinVolume(ctx context.Context, artifact BuiltinVolume) error {
	if err := ValidateBuiltinVolume(artifact); err != nil {
		return err
	}
	roles := slices.Clone(artifact.Roles)
	slices.Sort(roles)
	return s.write(ctx, func(tx *sql.Tx) error {
		exists, err := checkBuiltinRegistration(ctx, tx, artifact)
		if err != nil || exists {
			return err
		}
		state, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		var capacity int64
		if err = tx.QueryRowContext(ctx, `SELECT capacity FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='published'`, artifact.StoreID, artifact.PublicationPurpose, artifact.Reference.Digest).Scan(&capacity); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrArtifactUnavailable
			}
			return err
		}
		if capacity < artifact.MinimumBytes {
			return ErrInvalid
		}
		roleJSON, _ := json.Marshal(roles)
		entries, _ := json.Marshal(artifact.Entries)
		if _, err = tx.ExecContext(ctx, `INSERT INTO builtin_volumes(id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, artifact.Reference.ID, artifact.Reference.Version, artifact.Reference.Digest, artifact.StoreID, artifact.PublicationPurpose, artifact.MinimumBytes, string(roleJSON), artifact.CleanRuntime, string(entries), s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, "operator", "volume_artifact.register", "volume_artifact", artifact.Reference.ID+"/"+artifact.Reference.Version, "", 1, map[string]any{"reference": artifact.Reference, "roles": roles, "minimum_bytes": artifact.MinimumBytes})
	})
}
