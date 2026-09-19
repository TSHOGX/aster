package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) BindBridgeStream(ctx context.Context, backendID, sessionID, generationID, handle string, epoch uint64) error {
	if backendID == "" || sessionID == "" || generationID == "" || handle == "" || epoch == 0 {
		return fmt.Errorf("complete bridge process binding required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_generations WHERE generation_id=? AND session_id=?`, generationID, sessionID).Scan(&generation); err != nil {
		return err
	}
	if generation != 1 {
		return fmt.Errorf("bridge stream generation does not belong to session")
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO bridge_streams(backend_id,session_id,generation_id,process_handle,epoch,watermark) VALUES(?,?,?,?,?,0)`, backendID, sessionID, generationID, handle, epoch)
	if err != nil {
		return err
	}
	var storedSession, storedHandle string
	var storedEpoch uint64
	err = tx.QueryRowContext(ctx, `SELECT session_id,process_handle,epoch FROM bridge_streams WHERE backend_id=? AND generation_id=?`, backendID, generationID).Scan(&storedSession, &storedHandle, &storedEpoch)
	if err != nil {
		return err
	}
	if storedSession != sessionID || storedHandle != handle || storedEpoch != epoch {
		return fmt.Errorf("bridge stream execution binding conflict")
	}
	return tx.Commit()
}
func (s *Store) BridgeStreamWatermark(ctx context.Context, backendID, generationID string) (uint64, error) {
	var watermark uint64
	err := s.db.QueryRowContext(ctx, `SELECT watermark FROM bridge_streams WHERE backend_id=? AND generation_id=?`, backendID, generationID).Scan(&watermark)
	return watermark, err
}

// CommitBridgeStreamReceipt follows protocol acceptance and its durable domain
// transaction. A crash before this commit redelivers the same envelope, whose
// existing command/output identities make that replay idempotent.
func (s *Store) CommitBridgeStreamReceipt(ctx context.Context, backendID, generationID string, sequence uint64, digest string) error {
	if sequence == 0 || len(digest) != 64 {
		return fmt.Errorf("bridge receipt requires sequence and digest")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var watermark uint64
	err = tx.QueryRowContext(ctx, `SELECT watermark FROM bridge_streams WHERE backend_id=? AND generation_id=?`, backendID, generationID).Scan(&watermark)
	if err != nil {
		return err
	}
	if sequence <= watermark {
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT digest FROM bridge_stream_receipts WHERE backend_id=? AND generation_id=? AND sequence=?`, backendID, generationID, sequence).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) || err == nil && existing != digest {
			return fmt.Errorf("%w: contradictory bridge journal receipt", errDuplicateOutputSequenceMismatch)
		}
		return err
	}
	if sequence != watermark+1 {
		return fmt.Errorf("bridge journal gap: expected %d, got %d", watermark+1, sequence)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_stream_receipts(backend_id,generation_id,sequence,digest) VALUES(?,?,?,?)`, backendID, generationID, sequence, digest)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE bridge_streams SET watermark=? WHERE backend_id=? AND generation_id=? AND watermark=?`, sequence, backendID, generationID, watermark)
	if err != nil {
		return err
	}
	return tx.Commit()
}
