package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/sqlitex"
)

// ClaimContentPublication reserves the durable record for one authored revision
// before any engine volume exists. It performs no remote I/O: every stage below
// persists intent first and settles the observation afterwards.
//
// A repeated claim for the same content returns the existing row, including a
// completed publication, so a restart resumes instead of preparing again. A
// claim whose entries disagree with a stored revision of the same digest is a
// conflict rather than an overwrite; content identity must stay collision-proof
// in the record, not only in the hash.
func (s *Store) ClaimContentPublication(ctx context.Context, p content.Publication) (content.Publication, error) {
	var empty content.Publication
	if p.StoreID == "" || !content.ValidPurpose(p.Purpose) || !content.ValidDigest(p.RevisionDigest) || p.Capacity <= 0 || len(p.Entries) == 0 {
		return empty, fmt.Errorf("store identity, purpose, revision digest, capacity and entries required")
	}
	if _, err := checkedProduct(p.Capacity, 2); err != nil {
		return empty, err
	}
	entries, err := json.Marshal(p.Entries)
	if err != nil {
		return empty, err
	}
	if content.Digest(p.Entries) != p.RevisionDigest {
		return empty, fmt.Errorf("claimed entries do not produce the revision digest")
	}
	now := s.timestamp()
	tx, release, err := s.beginControl(ctx)
	if err != nil {
		return empty, err
	}
	defer release()
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(generation),-1)+1 FROM content_publication_attempts WHERE store_id=? AND purpose=? AND revision_digest=?`, p.StoreID, p.Purpose, p.RevisionDigest).Scan(&p.Generation); err != nil {
		return empty, err
	}
	insert, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO content_publications(store_id,purpose,revision_digest,capacity,entries,stage,created_at,updated_at,generation) VALUES(?,?,?,?,?,'preparing',?,?,?)`, p.StoreID, p.Purpose, p.RevisionDigest, p.Capacity, string(entries), now, now, p.Generation)
	if err != nil {
		return empty, releaseFenceError(err)
	}
	stored, err := publicationWith(ctx, tx, p.StoreID, p.Purpose, p.RevisionDigest)
	if err != nil {
		return empty, err
	}
	if stored.Capacity != p.Capacity || content.Digest(stored.Entries) != p.RevisionDigest {
		return empty, fmt.Errorf("existing publication of this revision declares different content or capacity")
	}
	if _, err = platformStorage(ctx, tx); err != nil {
		return empty, err
	}
	if count, err := insert.RowsAffected(); err != nil {
		return empty, err
	} else if count == 1 {
		if err = s.auditEvent(ctx, tx, "operator", "publication.claim", "publication", p.Purpose+"/"+p.RevisionDigest, "", 1, map[string]any{"store_id": p.StoreID, "capacity_bytes": p.Capacity}); err != nil {
			return empty, err
		}
	}
	return stored, tx.Commit()
}

// RecordContentStaging binds the prepared volume to the claim. The volume is
// recorded before it is populated so an interrupted preparation reuses the same
// engine object instead of leaking a new one on every retry.
func (s *Store) RecordContentStaging(ctx context.Context, p content.Publication, volume string) (content.Publication, error) {
	return s.advancePublication(ctx, p, "staged", func(stored content.Publication) error {
		if volume == "" {
			return fmt.Errorf("staging volume identity required")
		}
		if stored.StagingVolumeID != "" && stored.StagingVolumeID != volume {
			return fmt.Errorf("publication already staged on a different volume")
		}
		return nil
	}, func(ctx context.Context, tx *sql.Tx, p content.Publication, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE content_publications SET staging_volume_id=?,stage='staged',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage IN ('preparing','staged')`, volume, now, p.StoreID, p.Purpose, p.RevisionDigest)
		return err
	})
}

// RecordContentDerivation records the derived volume before it is verified. The
// volume is not yet a published reference: only verified bytes commit that.
func (s *Store) RecordContentDerivation(ctx context.Context, p content.Publication, volume string) (content.Publication, error) {
	return s.advancePublication(ctx, p, "derived", func(stored content.Publication) error {
		if volume == "" {
			return fmt.Errorf("derived volume identity required")
		}
		if stored.StagingVolumeID == "" {
			return fmt.Errorf("publication has no staged content to derive")
		}
		if stored.DerivedVolumeID != "" && stored.DerivedVolumeID != volume {
			return fmt.Errorf("publication already derived a different volume")
		}
		if stored.StagingVolumeID == volume {
			return fmt.Errorf("derived volume must differ from its staging volume")
		}
		return nil
	}, func(ctx context.Context, tx *sql.Tx, p content.Publication, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE content_publications SET derived_volume_id=?,stage='derived',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage IN ('populated','derived')`, volume, now, p.StoreID, p.Purpose, p.RevisionDigest)
		return err
	})
}

// CommitContentPublication publishes the verified volume. The caller must have
// read the published volume's own bytes back and matched the revision digest.
func (s *Store) CommitContentPublication(ctx context.Context, p content.Publication, volume, verified string) (content.Publication, error) {
	return s.advancePublication(ctx, p, "published", func(stored content.Publication) error {
		if volume == "" {
			return fmt.Errorf("published volume identity required")
		}
		if verified != stored.RevisionDigest {
			return fmt.Errorf("verified content digest does not match the published revision")
		}
		if stored.DerivedVolumeID != volume {
			return fmt.Errorf("committed volume differs from the derived volume")
		}
		if stored.PublishedVolumeID != "" && stored.PublishedVolumeID != volume {
			return fmt.Errorf("publication already committed a different volume")
		}
		return nil
	}, func(ctx context.Context, tx *sql.Tx, p content.Publication, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE content_publications SET published_volume_id=?,stage='published',error='',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage IN ('derived','published')`, volume, now, p.StoreID, p.Purpose, p.RevisionDigest)
		return err
	})
}

// FailContentPublication records a terminal, diagnosable preparation failure. It
// never clears a committed publication: published content stays published.
func (s *Store) FailContentPublication(ctx context.Context, p content.Publication, reason string) error {
	if reason == "" {
		return fmt.Errorf("publication failure requires a reason")
	}
	now := s.timestamp()
	tx, release, err := s.beginControl(ctx)
	if err != nil {
		return err
	}
	defer release()
	defer tx.Rollback()
	stored, err := publicationWith(ctx, tx, p.StoreID, p.Purpose, p.RevisionDigest)
	if err != nil {
		return err
	}
	if stored.Generation != p.Generation {
		return ErrConflict
	}
	if stored.Stage == "published" {
		return fmt.Errorf("published content cannot be marked failed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE content_publications SET stage='failed',error=?,updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage!='published'`, reason, now, p.StoreID, p.Purpose, p.RevisionDigest); err != nil {
		return err
	}
	return tx.Commit()
}

// FindPublishedContent resolves a purpose and revision to its published volume.
// It is the only read a launch path uses: an unpublished or failed revision has
// no volume and cannot be mounted.
func (s *Store) FindPublishedContent(ctx context.Context, storeID, purpose, revision string) (string, bool, error) {
	var volume string
	err := s.reader.QueryRowContext(ctx, `SELECT published_volume_id FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='published'`, storeID, purpose, revision).Scan(&volume)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return volume, volume != "", nil
}

// ListContentPublications reports every recorded revision for operability: which
// revisions are published, which preparation is unresolved, and how much pinned
// staging content has accumulated.
func (s *Store) ListContentPublications(ctx context.Context, storeID string) ([]content.Publication, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,error,created_at,updated_at,generation FROM content_publications WHERE store_id=? ORDER BY purpose,revision_digest LIMIT 500`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []content.Publication
	for rows.Next() {
		p, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) advancePublication(ctx context.Context, p content.Publication, stage string, check func(content.Publication) error, apply func(context.Context, *sql.Tx, content.Publication, string) error) (content.Publication, error) {
	var empty content.Publication
	now := s.timestamp()
	tx, release, err := s.beginControl(ctx)
	if err != nil {
		return empty, err
	}
	defer release()
	defer tx.Rollback()
	stored, err := publicationWith(ctx, tx, p.StoreID, p.Purpose, p.RevisionDigest)
	if err != nil {
		return empty, err
	}
	if stored.Generation != p.Generation {
		return empty, ErrConflict
	}
	if stored.Stage == "failed" {
		return empty, fmt.Errorf("publication previously failed: %s", stored.Error)
	}
	if err := check(stored); err != nil {
		return empty, err
	}
	if err := apply(ctx, tx, stored, now); err != nil {
		return empty, err
	}
	updated, err := publicationWith(ctx, tx, p.StoreID, p.Purpose, p.RevisionDigest)
	if err != nil {
		return empty, err
	}
	if updated.Stage != stage && !(stage == "derived" && updated.Stage == "published") {
		return empty, fmt.Errorf("publication stage did not advance to %s", stage)
	}
	if stored.Stage != "published" && updated.Stage == "published" {
		if err = s.auditEvent(ctx, tx, "operator", "publication.commit", "publication", p.Purpose+"/"+p.RevisionDigest, "", 1, map[string]any{"store_id": p.StoreID, "volume_id": updated.PublishedVolumeID}); err != nil {
			return empty, err
		}
	}
	return updated, tx.Commit()
}

func publicationWith(ctx context.Context, db queryer, storeID, purpose, revision string) (content.Publication, error) {
	row := db.QueryRowContext(ctx, `SELECT store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,error,created_at,updated_at,generation FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=?`, storeID, purpose, revision)
	return scanPublication(row)
}

func scanPublication(row scanner) (content.Publication, error) {
	var p content.Publication
	var entries, created, updated string
	if err := row.Scan(&p.StoreID, &p.Purpose, &p.RevisionDigest, &p.Capacity, &entries, &p.StagingVolumeID, &p.DerivedVolumeID, &p.PublishedVolumeID, &p.Stage, &p.Error, &created, &updated, &p.Generation); err != nil {
		return content.Publication{}, err
	}
	if err := json.Unmarshal([]byte(entries), &p.Entries); err != nil {
		return content.Publication{}, err
	}
	p.CreatedAt = sqlitex.ParseTime(created)
	p.UpdatedAt = sqlitex.ParseTime(updated)
	return p, nil
}

// StartContentPopulation records the uncertain unkeyed effect before sending any
// byte. A second owner/retry cannot repopulate or seal while an old writer might
// still act. Lease expiry alone is never evidence a remote writer stopped.
func (s *Store) StartContentPopulation(ctx context.Context, p content.Publication) (content.Publication, error) {
	return s.advancePublication(ctx, p, "populating", func(stored content.Publication) error {
		if stored.Stage != "staged" || stored.StagingVolumeID != p.StagingVolumeID {
			return content.ErrPublicationQuarantined
		}
		return nil
	}, func(ctx context.Context, tx *sql.Tx, p content.Publication, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE content_publications SET stage='populating',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='staged'`, now, p.StoreID, p.Purpose, p.RevisionDigest)
		return err
	})
}
func (s *Store) FinishContentPopulation(ctx context.Context, p content.Publication) (content.Publication, error) {
	return s.advancePublication(ctx, p, "populated", func(stored content.Publication) error {
		if stored.Stage != "populating" || stored.StagingVolumeID != p.StagingVolumeID {
			return content.ErrPublicationQuarantined
		}
		return nil
	}, func(ctx context.Context, tx *sql.Tx, p content.Publication, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE content_publications SET stage='populated',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='populating'`, now, p.StoreID, p.Purpose, p.RevisionDigest)
		return err
	})
}
func (s *Store) QuarantineContentPublication(ctx context.Context, p content.Publication) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE content_publications SET stage='quarantined',error='unsettled_write',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='populating' AND generation=?`, s.timestamp(), p.StoreID, p.Purpose, p.RevisionDigest, p.Generation)
		return err
	})
}

// RetryContentPublication retains the complete failed attempt and its charges,
// then admits a new generation with new engine keys. An old remote writer can
// only affect its quarantined staging object; no identity is silently reused.
func (s *Store) RetryContentPublication(ctx context.Context, store, purpose, digest string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return ErrDraining
		}
		p, err := publicationWith(ctx, tx, store, purpose, digest)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.Stage != "quarantined" && p.Stage != "populating" && p.Stage != "failed" {
			return nil
		}
		if p.Generation >= 3 {
			return Admission("publication_attempt_limit", ErrCapacity)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO content_publication_attempts VALUES(?,?,?,?,?,?,?,'quarantined',?)`, p.StoreID, p.Purpose, p.RevisionDigest, p.Generation, p.Capacity, p.StagingVolumeID, p.DerivedVolumeID, p.Error); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE content_publications SET generation=generation+1,stage='preparing',staging_volume_id='',derived_volume_id='',published_volume_id='',error='',updated_at=? WHERE store_id=? AND purpose=? AND revision_digest=?`, s.timestamp(), store, purpose, digest)
		return err
	})
}
