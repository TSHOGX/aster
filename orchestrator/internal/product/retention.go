package product

import (
	"context"
	"database/sql"
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

// PruneMetadata bounds replay/transfer metadata while preserving live Session
// conversation and original uncertain operations. Event cursors outside this
// retained window cause the browser to refresh authoritative snapshots.
func (s *Store) PruneMetadata(ctx context.Context) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at<=?`, s.timestamp()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id IN (SELECT id FROM (SELECT id,row_number() OVER (PARTITION BY project_id ORDER BY id DESC) AS n FROM events) WHERE n>10000 LIMIT 1000)`); err != nil {
			return err
		}
		// IDs of ambiguous mutations remain queryable; successful metadata/read
		// operations older than a day carry no retry authority or file bytes.
		_, err := tx.ExecContext(ctx, `DELETE FROM file_operations WHERE id IN (SELECT id FROM file_operations WHERE settled=1 AND state IN ('succeeded','failed') AND json_extract(request,'$.operation') IN ('list','read') AND updated_at<? LIMIT 1000)`, sqlitex.FormatTime(s.now().Add(-24*time.Hour)))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM project_activity WHERE id IN (SELECT a.id FROM project_activity a WHERE a.kind='files' AND a.state='complete' AND NOT EXISTS(SELECT 1 FROM file_operations f WHERE 'file:'||f.id=a.id) LIMIT 1000)`)
		return err
	})
}
