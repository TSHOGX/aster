package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxStoredArtifactBytes bounds this database-backed artifact path. Larger corpus
// transports must use a separately bounded streaming store, not bypass this limit.
const MaxStoredArtifactBytes int64 = 64 << 20

type artifactQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Exact dataset identities and byte aliases remain private to assigned evaluation
// jobs. Recheck at use/download because a later profile can protect retained bytes.
func requireNonDatasetArtifact(ctx context.Context, q artifactQuery, ref evolution.ArtifactRef) error {
	var protected bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_definitions WHERE kind='profile' AND (json_extract(body,'$.dataset.digest')=? OR (json_extract(body,'$.dataset.group_id')=? AND json_extract(body,'$.dataset.id')=?)))`, ref.Digest, ref.GroupID, ref.ID).Scan(&protected)
	if err != nil {
		return err
	}
	if protected {
		return ErrNotFound
	}
	return nil
}

// NonDatasetArtifact reads generation artifacts or public reports in one snapshot
// with the held-out exclusion. Current run/source grants are the caller's duty.
// Private evaluator input and already-dispatched cleanup retain Artifact access.
func (s *Store) NonDatasetArtifact(ctx context.Context, ref evolution.ArtifactRef) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = requireNonDatasetArtifact(ctx, tx, ref); err != nil {
		return nil, err
	}
	return readArtifact(ctx, tx, ref)
}

func requireSealedGenerationArtifact(ctx context.Context, tx *sql.Tx, ref evolution.ArtifactRef) error {
	if err := requireNonDatasetArtifact(ctx, tx, ref); err != nil {
		return err
	}
	return requireSealedArtifact(ctx, tx, ref)
}

// SealArtifact stores already-authorized artifact bytes and immutable provenance.
// It is not an export grant; public callers must check current source/use authority.
func (s *Store) SealArtifact(ctx context.Context, ref evolution.ArtifactRef, input io.Reader) error {
	if ref.Validate() != nil || ref.Size > MaxStoredArtifactBytes {
		return fmt.Errorf("invalid artifact reference or size")
	}
	raw, err := io.ReadAll(io.LimitReader(input, ref.Size+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) != ref.Size || evolution.Digest(raw) != ref.Digest {
		return fmt.Errorf("artifact size or digest mismatch")
	}
	metadata, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = storeSealedArtifact(ctx, tx, ref, metadata, raw); err != nil {
		return err
	}
	return tx.Commit()
}

// storeSealedArtifact participates in the caller's transaction. The caller has
// validated ref, metadata, exact byte length and digest before entering here.
func storeSealedArtifact(ctx context.Context, tx *sql.Tx, ref evolution.ArtifactRef, metadata, raw []byte) error {
	var prior []byte
	err := tx.QueryRowContext(ctx, "SELECT metadata FROM artifacts WHERE group_id=? AND id=?", ref.GroupID, ref.ID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, metadata) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var used int64
	if err = tx.QueryRowContext(ctx, "SELECT used_bytes FROM artifact_store_quota WHERE id=1").Scan(&used); err != nil {
		return err
	}
	if ref.Size > MaxArtifactStoreBytes-used {
		return fmt.Errorf("artifact store quota exceeded")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE artifact_store_quota SET used_bytes=used_bytes+? WHERE id=1", ref.Size); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO artifacts(group_id,id,metadata,body) VALUES(?,?,?,?)", ref.GroupID, ref.ID, metadata, raw); err != nil {
		return err
	}
	return nil
}

// Artifact verifies exact immutable reference identity on read. Callers must
// authorize current artifact grants before returning bytes outside the service.
func (s *Store) Artifact(ctx context.Context, ref evolution.ArtifactRef) ([]byte, error) {
	return readArtifact(ctx, s.db, ref)
}

func readArtifact(ctx context.Context, q artifactQuery, ref evolution.ArtifactRef) ([]byte, error) {
	if ref.Validate() != nil || ref.Size > MaxStoredArtifactBytes {
		return nil, fmt.Errorf("invalid artifact reference")
	}
	var metadata, raw []byte
	err := q.QueryRowContext(ctx, "SELECT metadata,body FROM artifacts WHERE group_id=? AND id=?", ref.GroupID, ref.ID).Scan(&metadata, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	stored, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(metadata))
	if err != nil || stored != ref {
		return nil, ErrConflict
	}
	if int64(len(raw)) != ref.Size || evolution.Digest(raw) != ref.Digest {
		return nil, fmt.Errorf("stored artifact integrity mismatch")
	}
	return raw, nil
}

func requireSealedArtifact(ctx context.Context, tx *sql.Tx, ref evolution.ArtifactRef) error {
	var metadata []byte
	var size int64
	if err := tx.QueryRowContext(ctx, "SELECT metadata,length(body) FROM artifacts WHERE group_id=? AND id=?", ref.GroupID, ref.ID).Scan(&metadata, &size); err != nil {
		return err
	}
	stored, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(metadata))
	if err != nil || stored != ref || size != ref.Size {
		return fmt.Errorf("sealed artifact identity mismatch")
	}
	return nil
}

// Inline attempts can contain copies of inputs and provider outputs. Once any
// retained source is registered as held-out, deny that run's inline trace/replay
// rather than trying to redact individual fields or infer transformed secrets.
func requireNonDatasetRun(ctx context.Context, q artifactQuery, group, run string) error {
	var protected bool
	err := q.QueryRowContext(ctx, advertisedRunArtifacts+`SELECT
 EXISTS(SELECT 1 FROM runs WHERE id=:run AND group_id=:group) AND
 EXISTS(SELECT 1 FROM catalog_definitions d JOIN (
 SELECT metadata FROM refs
 UNION ALL SELECT j.value FROM runs r, json_each(r.body,'$.pins.inputs') j WHERE r.id=:run
 ) a ON d.kind='profile' AND (json_extract(d.body,'$.dataset.digest')=json_extract(a.metadata,'$.digest') OR
 (json_extract(d.body,'$.dataset.group_id')=json_extract(a.metadata,'$.group_id') AND json_extract(d.body,'$.dataset.id')=json_extract(a.metadata,'$.id'))))`, sql.Named("run", run), sql.Named("group", group)).Scan(&protected)
	if err != nil {
		return err
	}
	if protected {
		return ErrNotFound
	}
	return nil
}

func (s *Store) beginGenerationRead(ctx context.Context, group, run string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if err = requireNonDatasetRun(ctx, tx, group, run); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}
