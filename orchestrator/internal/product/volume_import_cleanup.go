package product

import (
	"context"
	"database/sql"
	"errors"

	"harness-platform/orchestrator/internal/content"
)

// RetireImportedVolume atomically checks every saved consumer binding before
// removing this registry alias. Engine deletion is a separate observed phase.
func (s *Store) RetireImportedVolume(ctx context.Context, token, id, requestID string) (VolumeImport, error) {
	request, err := mutationRequest(requestID, "volume.import.retire", id, map[string]string{"id": id})
	if err != nil {
		return VolumeImport{}, err
	}
	var out VolumeImport
	err = s.write(ctx, func(tx *sql.Tx) error {
		a, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		_, replay, err := readAdminReceipt(ctx, tx, a.ID, request)
		if err != nil {
			return err
		}
		out, err = scanImport(tx.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id=?", id))
		if err != nil || replay {
			return err
		}
		if out.State != "ready" && out.State != "canceled" && out.State != "failed" {
			return ErrConflict
		}
		state, err := registryState(ctx, tx, "volume", out.Artifact)
		if err == nil {
			if state.TemplateReferenced || state.ProjectReferences > 0 || state.PendingReferences > 0 {
				return Admission("runtime_references_block_retirement", ErrConflict)
			}
			if _, err = tx.ExecContext(ctx, "UPDATE builtin_volumes SET enabled=0,retired_at=?,revision=revision+1 WHERE id=? AND version=?", s.timestamp(), out.Artifact.ID, out.Artifact.Version); err != nil {
				return err
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		out.State = "retiring"
		if _, err = tx.ExecContext(ctx, "UPDATE volume_imports SET state='retiring',error='',updated_at=? WHERE id=?", s.timestamp(), id); err != nil {
			return err
		}
		if err = s.recordImportReceipt(ctx, tx, a.ID, request, id); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, a.ID, "volume.import.retire", "volume_import", id, requestID, 1, nil)
	})
	return out, err
}
func (s *Store) NextImportCleanup(ctx context.Context) (VolumeImport, error) {
	return scanImport(s.reader.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE state='retiring' OR (state IN ('ready','canceled','retired') AND transfer_reserved>0) ORDER BY updated_at,id LIMIT 1"))
}

// PublicationCleanup refuses deletion while any registry alias or pending import
// still needs the shared content. It never consults Studio source state.
func (s *Store) PublicationCleanup(ctx context.Context, i VolumeImport) (content.Publication, bool, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return content.Publication{}, false, err
	}
	defer tx.Rollback()
	var count int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM builtin_volumes WHERE store_id=? AND publication_purpose=? AND digest=? AND retired_at='')+(SELECT count(*) FROM volume_imports WHERE store_id=? AND content_profile=? AND tree_digest=? AND id<>? AND state NOT IN ('retiring','retired','canceled'))`, i.StoreID, i.PublicationPurpose(), i.TreeDigest, i.StoreID, i.Profile, i.TreeDigest, i.ID).Scan(&count)
	if err != nil {
		return content.Publication{}, false, err
	}
	if count != 0 {
		return content.Publication{}, false, nil
	}
	p, err := publicationWith(ctx, tx, i.StoreID, i.PublicationPurpose(), i.TreeDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return content.Publication{}, false, nil
	}
	return p, err == nil, err
}
func (s *Store) RemoveContentPublication(ctx context.Context, p content.Publication) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM builtin_volumes WHERE store_id=? AND publication_purpose=? AND digest=? AND retired_at=''", p.StoreID, p.Purpose, p.RevisionDigest).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrConflict
		}
		// Keep an absent attempt tombstone and all source provenance, but remove the
		// active allocation row only after observing both engine IDs absent.
		if _, err := tx.ExecContext(ctx, `INSERT INTO content_publication_attempts(store_id,purpose,revision_digest,generation,capacity,staging_volume_id,derived_volume_id,state) VALUES(?,?,?,?,?,?,?,'absent') ON CONFLICT(store_id,purpose,revision_digest,generation) DO UPDATE SET state='absent'`, p.StoreID, p.Purpose, p.RevisionDigest, p.Generation, p.Capacity, p.StagingVolumeID, p.DerivedVolumeID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND generation=?", p.StoreID, p.Purpose, p.RevisionDigest, p.Generation)
		return err
	})
}
func (s *Store) FinishImportRetirement(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE volume_imports SET state='retired',error='',updated_at=? WHERE id=? AND state='retiring'", s.timestamp(), id)
		return err
	})
}
func (s *Store) ImportCleanupError(ctx context.Context, id, code string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE volume_imports SET error=?,updated_at=? WHERE id=?", code, s.timestamp(), id)
		return err
	})
}

type ContentAttempt struct {
	Publication content.Publication
	State       string
}

func (s *Store) QuarantinedAttempts(ctx context.Context, store, purpose, digest string) ([]ContentAttempt, error) {
	rows, err := s.reader.QueryContext(ctx, "SELECT generation,capacity,staging_volume_id,derived_volume_id,state FROM content_publication_attempts WHERE store_id=? AND purpose=? AND revision_digest=? AND state!='absent' ORDER BY generation", store, purpose, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentAttempt{}
	for rows.Next() {
		a := ContentAttempt{Publication: content.Publication{StoreID: store, Purpose: purpose, RevisionDigest: digest}}
		if err = rows.Scan(&a.Publication.Generation, &a.Publication.Capacity, &a.Publication.StagingVolumeID, &a.Publication.DerivedVolumeID, &a.State); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) SettleContentAttempt(ctx context.Context, p content.Publication) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE content_publication_attempts SET state='absent',error='' WHERE store_id=? AND purpose=? AND revision_digest=? AND generation=?", p.StoreID, p.Purpose, p.RevisionDigest, p.Generation)
		return err
	})
}

func (s *Store) RecordCleanupObjects(ctx context.Context, p content.Publication) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		existing, err := publicationWith(ctx, tx, p.StoreID, p.Purpose, p.RevisionDigest)
		if err != nil {
			return err
		}
		if existing.Generation != p.Generation {
			return ErrConflict
		}
		if existing.StagingVolumeID != "" && existing.StagingVolumeID != p.StagingVolumeID || existing.DerivedVolumeID != "" && existing.DerivedVolumeID != p.DerivedVolumeID {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE content_publications SET staging_volume_id=?,derived_volume_id=? WHERE store_id=? AND purpose=? AND revision_digest=? AND generation=?", p.StagingVolumeID, p.DerivedVolumeID, p.StoreID, p.Purpose, p.RevisionDigest, p.Generation)
		return err
	})
}
