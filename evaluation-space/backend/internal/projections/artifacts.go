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
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT metadata FROM artifacts WHERE group_id=? AND id=?", ref.GroupID, ref.ID).Scan(&prior)
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
	return tx.Commit()
}

// Artifact verifies exact immutable reference identity on read. Callers must
// authorize current artifact grants before returning bytes outside the service.
func (s *Store) Artifact(ctx context.Context, ref evolution.ArtifactRef) ([]byte, error) {
	if ref.Validate() != nil || ref.Size > MaxStoredArtifactBytes {
		return nil, fmt.Errorf("invalid artifact reference")
	}
	var metadata, raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT metadata,body FROM artifacts WHERE group_id=? AND id=?", ref.GroupID, ref.ID).Scan(&metadata, &raw)
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
