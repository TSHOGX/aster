package formatconvert

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite"

	"harness-platform/orchestrator/internal/product"
)

const SourceVersion = 1
const SourceDigest = "sha256:c945336d67883b9452c1d0e5f5e591bec42d981fafd12f330a727d1765d85983"

func Inspect(ctx context.Context, path string) (int, string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return 0, "", err
	}
	q, err := sql.Open("sqlite", fileURL(path, "mode=ro"))
	if err != nil {
		return 0, "", err
	}
	defer q.Close()
	var version int
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, "", err
	}
	fingerprint, err := digest(ctx, q, "main")
	return version, fingerprint, err
}

func fileURL(path string, query string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}

func digest(ctx context.Context, q *sql.DB, catalog string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT type,name,tbl_name,coalesce(sql,'') FROM `+catalog+`.sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var typ, name, table, ddl string
		if err := rows.Scan(&typ, &name, &table, &ddl); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", typ, name, table, ddl)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), rows.Err()
}

func identifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func checkZero(ctx context.Context, q *sql.DB, query string) error {
	var count int
	if err := q.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("source is not quiescent or has unsupported evidence (%d rows)", count)
	}
	return nil
}

func verifySource(ctx context.Context, q *sql.DB) error {
	var version int
	if err := q.QueryRowContext(ctx, `PRAGMA source.user_version`).Scan(&version); err != nil {
		return err
	}
	fingerprint, err := digest(ctx, q, "source")
	if err != nil {
		return err
	}
	if version != SourceVersion || fingerprint != SourceDigest {
		return fmt.Errorf("unsupported source format: version %d, fingerprint %s", version, fingerprint)
	}
	var integrity string
	if err := q.QueryRowContext(ctx, `PRAGMA source.integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("source integrity: %s", integrity)
	}
	checks := []struct{ name, query string }{
		{"live Sessions", `SELECT count(*) FROM source.sessions WHERE state<>'idle'`},
		{"live incarnations", `SELECT count(*) FROM source.agent_incarnations WHERE state IN ('starting','running','stopping','unknown')`},
		{"unsettled turns", `SELECT count(*) FROM source.turns WHERE state NOT IN ('held','queued','canceled','completed','failed')`},
		{"pending commands", `SELECT count(*) FROM source.agent_commands WHERE state IN ('pending','sending','dispatching','unknown')`},
		{"unsettled normal commands", `SELECT count(*) FROM source.agent_commands WHERE kind='prompt' AND state<>'completed'`},
		{"unsettled Steer commands", `SELECT count(*) FROM source.agent_commands WHERE kind='steer' AND state NOT IN ('applied','rejected')`},
		{"open service slots", `SELECT count(*) FROM source.service_request_slots WHERE closed=0`},
		{"active service grants", `SELECT count(*) FROM source.service_grants WHERE state='active'`},
	}
	for _, check := range checks {
		if err := checkZero(ctx, q, check.query); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
	}
	if err := checkZero(ctx, q, `SELECT count(*) FROM source.agent_commands steer WHERE steer.kind='steer' AND steer.state='applied' AND
 (SELECT count(*) FROM source.agent_commands prompt JOIN source.turns t ON t.id=prompt.turn_id
 WHERE prompt.kind='prompt' AND prompt.session_id=steer.session_id AND prompt.incarnation_id=steer.incarnation_id
 AND prompt.created_at<=steer.created_at AND t.updated_at>=steer.created_at)<>1`); err != nil {
		return fmt.Errorf("applied Steer target is ambiguous: %w", err)
	}
	rows, err := q.QueryContext(ctx, `PRAGMA source.foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("source foreign keys are invalid")
	}
	return rows.Err()
}

func columns(ctx context.Context, tx *sql.Tx, catalog, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA `+catalog+`.table_info(`+identifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var cid, notNull, key int
		var name, typ string
		var fallback sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &fallback, &key); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func derived(name string) bool {
	return strings.HasPrefix(name, "message_search") ||
		(strings.HasPrefix(name, "conversation_") && name != "conversation_grants" && name != "conversation_sources")
}

func copyTables(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM source.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !derived(name) && name != "application_release" && name != "turns" {
			tables = append(tables, name)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, table := range tables {
		old, err := columns(ctx, tx, "source", table)
		if err != nil {
			return err
		}
		current, err := columns(ctx, tx, "main", table)
		if err != nil {
			return err
		}
		if len(current) == 0 {
			return fmt.Errorf("source table %q has no destination", table)
		}
		selected := make([]string, 0, len(old))
		for _, name := range old {
			if slices.Contains(current, name) {
				selected = append(selected, identifier(name))
			}
		}
		if table == "messages" {
			selected = append([]string{"rowid"}, selected...)
		}
		quoted := strings.Join(selected, ",")
		if _, err := tx.ExecContext(ctx, `INSERT INTO main.`+identifier(table)+`(`+quoted+`) SELECT `+quoted+` FROM source.`+identifier(table)); err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
		var before, after int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source.`+identifier(table)).Scan(&before); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.`+identifier(table)).Scan(&after); err != nil {
			return err
		}
		if before != after {
			return fmt.Errorf("%s row count changed: %d to %d", table, before, after)
		}
	}
	return nil
}

func convert(ctx context.Context, q *sql.DB) error {
	if err := verifySource(ctx, q); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	tx, err := q.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM main.sqlite_master WHERE type='trigger' ORDER BY name`)
	if err != nil {
		return err
	}
	var triggerSQL, triggerNames []string
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			rows.Close()
			return err
		}
		triggerSQL = append(triggerSQL, ddl)
		triggerNames = append(triggerNames, name)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, name := range triggerNames {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER main.`+identifier(name)); err != nil {
			return err
		}
	}
	if err := copyTables(ctx, tx); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,queue_policy,disposition,sequence,created_at,updated_at)
 SELECT t.id,t.project_id,t.session_id,t.request_id,t.content,t.initially_held,
 CASE WHEN t.state='held' THEN 'held' ELSE 'released' END,
 CASE t.state WHEN 'held' THEN 'pending' WHEN 'queued' THEN 'pending' WHEN 'canceled' THEN 'canceled'
 ELSE CASE WHEN EXISTS(SELECT 1 FROM source.agent_commands c WHERE c.turn_id=t.id AND c.kind='steer' AND c.state='applied') THEN 'claimed' ELSE 'historical' END END,
 t.sequence,t.created_at,t.updated_at FROM source.turns t`)
	if err != nil {
		return fmt.Errorf("copy prompts: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_runs(id,project_id,session_id,prompt_id,incarnation_id,owner,state,outcome,started_at,settled_at)
 SELECT c.id,c.project_id,c.session_id,c.turn_id,c.incarnation_id,i.owner,
 CASE WHEN EXISTS(SELECT 1 FROM source.agent_commands steer WHERE steer.kind='steer' AND steer.state='applied' AND steer.session_id=c.session_id AND steer.incarnation_id=c.incarnation_id AND steer.created_at>=c.created_at
 AND c.id=(SELECT p.id FROM source.agent_commands p WHERE p.kind='prompt' AND p.session_id=steer.session_id AND p.incarnation_id=steer.incarnation_id AND p.created_at<=steer.created_at ORDER BY p.created_at DESC,p.id DESC LIMIT 1)) THEN 'unknown' ELSE 'settled' END,
 CASE WHEN EXISTS(SELECT 1 FROM source.agent_commands steer WHERE steer.kind='steer' AND steer.state='applied' AND steer.session_id=c.session_id AND steer.incarnation_id=c.incarnation_id AND steer.created_at>=c.created_at
 AND c.id=(SELECT p.id FROM source.agent_commands p WHERE p.kind='prompt' AND p.session_id=steer.session_id AND p.incarnation_id=steer.incarnation_id AND p.created_at<=steer.created_at ORDER BY p.created_at DESC,p.id DESC LIMIT 1)) THEN 'unknown' ELSE t.state END,
 c.created_at,CASE WHEN t.state IN ('completed','failed','canceled') THEN t.updated_at ELSE '' END
 FROM source.agent_commands c JOIN source.turns t ON t.id=c.turn_id JOIN source.agent_incarnations i ON i.id=c.incarnation_id WHERE c.kind='prompt'`)
	if err != nil {
		return fmt.Errorf("copy runs: %w", err)
	}
	var unmapped int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source.agent_commands c WHERE c.kind='steer' AND c.state='applied' AND NOT EXISTS(
 SELECT 1 FROM execution_runs r WHERE r.session_id=c.session_id AND r.incarnation_id=c.incarnation_id AND r.started_at<=c.created_at)`).Scan(&unmapped); err != nil {
		return err
	}
	if unmapped != 0 {
		return fmt.Errorf("%d applied Steers lack a correlated source run", unmapped)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO input_deliveries(id,project_id,session_id,prompt_id,request_id,command_id,run_id,input_token,channel,state,acceptance_sequence,accepted_at)
 SELECT 'import/'||c.id,c.project_id,c.session_id,c.turn_id,'import/'||c.id,c.id,
 (SELECT r.id FROM execution_runs r JOIN turns t ON t.id=r.prompt_id WHERE r.session_id=c.session_id AND r.incarnation_id=c.incarnation_id AND r.started_at<=c.created_at AND t.updated_at>=c.created_at ORDER BY r.started_at DESC,r.id DESC LIMIT 1),
 'import/'||c.id,'steer','unknown',row_number() OVER (PARTITION BY c.session_id ORDER BY c.created_at,c.id),c.created_at
 FROM source.agent_commands c WHERE c.kind='steer' AND c.state='applied'`)
	if err != nil {
		return fmt.Errorf("retain uncertain Steers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state='blocked' WHERE id IN (SELECT session_id FROM input_deliveries WHERE state='unknown')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_pending_sessions SELECT id FROM sessions`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_search(message_search) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild search: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_release SET generation=(SELECT generation FROM source.application_release WHERE id=1),owner='',phase='open',deadline='',reason='converted_offline',updated_at=(SELECT updated_at FROM source.application_release WHERE id=1),current_instance='',activation_instance='',target_digest='',finished_digest='',mode='quiescent',compatibility_digest='',schema_digest='',gateway_snapshot='' WHERE id=1`); err != nil {
		return err
	}
	for _, ddl := range triggerSQL {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("restore trigger: %w", err)
		}
	}
	return tx.Commit()
}

// Convert writes a separate current-format database from a quiescent copy of
// the measured version-1 source. The supplied source must be an offline backup.
func Convert(ctx context.Context, sourcePath, destinationPath string) (err error) {
	sourcePath, err = filepath.Abs(sourcePath)
	if err != nil {
		return err
	}
	destinationPath, err = filepath.Abs(destinationPath)
	if err != nil {
		return err
	}
	if sourcePath == destinationPath {
		return errors.New("source and destination must differ")
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source must be a regular database backup")
	}
	if wal, err := os.Stat(sourcePath + "-wal"); err == nil {
		if wal.Size() != 0 {
			return errors.New("source backup has an uncheckpointed SQLite WAL")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return errors.New("destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	directory, err := os.MkdirTemp(filepath.Dir(destinationPath), ".queued-format-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	stage := filepath.Join(directory, "product.db")
	store, err := product.Open(ctx, stage)
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	q, err := sql.Open("sqlite", fileURL(stage, "mode=rw"))
	if err != nil {
		return err
	}
	q.SetMaxOpenConns(1)
	defer q.Close()
	if _, err := q.ExecContext(ctx, `ATTACH DATABASE ? AS source`, fileURL(sourcePath, "mode=ro&immutable=1")); err != nil {
		return err
	}
	if err := convert(ctx, q); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	if rows.Next() {
		rows.Close()
		return errors.New("converted foreign keys are invalid")
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	fingerprint, err := digest(ctx, q, "main")
	if err != nil {
		return err
	}
	_, expected, err := product.SchemaIdentity()
	if err != nil {
		return err
	}
	if fingerprint != expected {
		return fmt.Errorf("converted schema differs: %s", fingerprint)
	}
	if _, err := q.ExecContext(ctx, `DETACH DATABASE source`); err != nil {
		return err
	}
	if err := q.Close(); err != nil {
		return err
	}
	if err := Rebuild(ctx, stage); err != nil {
		return err
	}
	checkpoint, err := sql.Open("sqlite", fileURL(stage, "mode=rw"))
	if err != nil {
		return err
	}
	if _, err := checkpoint.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		checkpoint.Close()
		return err
	}
	if err := checkpoint.Close(); err != nil {
		return err
	}
	return os.Rename(stage, destinationPath)
}

// Rebuild projects only derived conversation state in a converted staging copy.
// It never replays an Agent command.
func Rebuild(ctx context.Context, path string) error {
	store, err := product.Open(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	q, err := sql.Open("sqlite", fileURL(path, "mode=ro"))
	if err != nil {
		return err
	}
	defer q.Close()
	rows, err := q.QueryContext(ctx, `SELECT s.id,s.project_id,p.owner_id FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.deletion_intent<2 ORDER BY s.id`)
	if err != nil {
		return err
	}
	type session struct{ id, project, owner string }
	var sessions []session
	for rows.Next() {
		var item session
		if err := rows.Scan(&item.id, &item.project, &item.owner); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range sessions {
		for n := 0; n < 10000; n++ {
			if err := store.ProjectConversation(ctx, item.id); err != nil && !errors.Is(err, product.ErrPending) && !errors.Is(err, product.ErrConflict) {
				return fmt.Errorf("rebuild Session %s: %w", item.id, err)
			}
			head, err := store.ConversationHead(ctx, product.Scope{Actor: item.owner, ProjectID: item.project}, item.id)
			if err != nil {
				return err
			}
			if head.State == "ready" && head.ProjectedFrontier == head.SourceFrontier {
				break
			}
			if head.State == "failed" || n == 9999 {
				return fmt.Errorf("Session %s projection did not settle: %s", item.id, head.State)
			}
		}
	}
	return nil
}
