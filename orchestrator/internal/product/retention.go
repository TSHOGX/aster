package product

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

const DisplayReplayBytes int64 = 16 << 20
const DisplayReplayItems = 8192
const RetentionBatch = 256

type retentionCursor struct {
	mu               sync.Mutex
	project, session string
}

func (s *Store) displayPressure(ctx context.Context, session string) (bool, error) {
	var pressure bool
	err := s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_retention WHERE session_id=? AND (pruning=1 OR change_count>? OR change_bytes>?))`, session, DisplayReplayItems, DisplayReplayBytes).Scan(&pressure)
	return pressure, err
}

// PruneConversation deletes a bounded prefix only. Its actual revision/ordinal
// frontier commits with the deletion. Source and current item detail are retained.
func (s *Store) PruneConversation(ctx context.Context, session string) error {
	pressure, err := s.displayPressure(ctx, session)
	if err != nil {
		return err
	}
	if !pressure {
		var garbage bool
		if err = s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_detail_gc WHERE session_id=?)`, session).Scan(&garbage); err != nil {
			return err
		}
		if !garbage {
			return nil
		}
	}
	return s.writeClass(ctx, backgroundWrite, "", 0, func(tx *sql.Tx) error { return pruneConversation(ctx, tx, session) })
}
func pruneConversation(ctx context.Context, tx *sql.Tx, session string) error {
	var count, bytes int64
	var pruning bool
	err := tx.QueryRowContext(ctx, `SELECT change_count,change_bytes,pruning FROM conversation_retention WHERE session_id=?`, session).Scan(&count, &bytes, &pruning)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if pruning || count > DisplayReplayItems || bytes > DisplayReplayBytes {
		rows, err := tx.QueryContext(ctx, `SELECT revision,ordinal,retained_bytes FROM conversation_changes WHERE session_id=? ORDER BY revision,ordinal LIMIT ?`, session, RetentionBatch)
		if err != nil {
			return err
		}
		var revision int64
		ordinal := -1
		for rows.Next() {
			var size int64
			if err = rows.Scan(&revision, &ordinal, &size); err != nil {
				break
			}
			bytes -= size
			count--
			if count <= DisplayReplayItems/2 && bytes <= DisplayReplayBytes/2 {
				break
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if ordinal >= 0 {
			if _, err = tx.ExecContext(ctx, `DELETE FROM conversation_changes WHERE session_id=? AND (revision,ordinal)<=(?,?)`, session, revision, ordinal); err != nil {
				return err
			}
		}
		stillPruning := count > DisplayReplayItems/2 || bytes > DisplayReplayBytes/2
		if _, err = tx.ExecContext(ctx, `UPDATE conversation_retention SET pruning=? WHERE session_id=? AND pruning<>?`, stillPruning, session, stillPruning); err != nil {
			return err
		}
	}
	// Scan only explicit obsolete candidates, not the entire current detail set.
	rows, err := tx.QueryContext(ctx, `SELECT item_id,item_revision FROM conversation_detail_gc WHERE session_id=? ORDER BY item_id,item_revision LIMIT ?`, session, RetentionBatch)
	if err != nil {
		return err
	}
	type version struct {
		id       string
		revision int64
	}
	versions := []version{}
	for rows.Next() {
		var v version
		if err = rows.Scan(&v.id, &v.revision); err != nil {
			break
		}
		versions = append(versions, v)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, v := range versions {
		if _, err = tx.ExecContext(ctx, `DELETE FROM conversation_details WHERE session_id=? AND item_id=? AND item_revision=? AND NOT EXISTS(SELECT 1 FROM conversation_items WHERE session_id=? AND id=? AND item_revision=?) AND NOT EXISTS(SELECT 1 FROM conversation_changes WHERE session_id=? AND item_id=? AND revision=?)`, session, v.id, v.revision, session, v.id, v.revision, session, v.id, v.revision); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM conversation_detail_gc WHERE session_id=? AND item_id=? AND item_revision=?`, session, v.id, v.revision); err != nil {
			return err
		}
	}
	return nil
}

// PruneMetadata rotates through bounded scopes without ranking the global outbox.
func (s *Store) PruneMetadata(ctx context.Context) error {
	s.retention.mu.Lock()
	defer s.retention.mu.Unlock()
	projects, err := s.retentionIDs(ctx, `SELECT id FROM projects WHERE id>? ORDER BY id LIMIT 8`, s.retention.project)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		s.retention.project = ""
	} else {
		s.retention.project = projects[len(projects)-1]
	}
	pressure, err := s.retentionIDs(ctx, `SELECT project_id FROM project_event_frontiers WHERE event_count>10000 AND project_id<>? ORDER BY event_count DESC,project_id LIMIT 8`, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	combined := []string{}
	for _, id := range append(pressure, projects...) {
		if !seen[id] {
			seen[id] = true
			combined = append(combined, id)
		}
	}
	projects = combined
	sessions, err := s.retentionIDs(ctx, `SELECT id FROM sessions WHERE id>? ORDER BY id LIMIT 8`, s.retention.session)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		s.retention.session = ""
	} else {
		s.retention.session = sessions[len(sessions)-1]
	}
	if err = s.writeClass(ctx, backgroundWrite, "", 0, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_digest IN (SELECT token_digest FROM auth_sessions WHERE expires_at<=? LIMIT ?)`, s.timestamp(), RetentionBatch)
		return err
	}); err != nil {
		return err
	}
	for _, project := range projects {
		var pressure bool
		if err = s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_event_frontiers WHERE project_id=? AND event_count>10000)`, project).Scan(&pressure); err != nil {
			return err
		}
		if !pressure {
			continue
		}
		if err = s.writeClass(ctx, backgroundWrite, "", 0, func(tx *sql.Tx) error {
			var cutoff int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM events WHERE project_id=? ORDER BY id DESC LIMIT 1 OFFSET 9999`, project).Scan(&cutoff)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE id IN (SELECT id FROM events WHERE project_id=? AND id<? ORDER BY id LIMIT ?)`, project, cutoff, 4*RetentionBatch)
			return err
		}); err != nil {
			return err
		}
	}
	for _, session := range sessions {
		if err = s.PruneConversation(ctx, session); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) retentionIDs(ctx context.Context, query, after string) ([]string, error) {
	rows, err := s.reader.QueryContext(ctx, query, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
