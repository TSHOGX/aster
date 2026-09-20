package store

import (
	"errors"
	"harness-platform/orchestrator/internal/sqlitex"
	"net/url"
	"os"

	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"fmt"
)

type dbRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

//go:embed schema.sql
var currentSchema string

const schemaFingerprint = "4cd6d4e3e2a41e5b57dee067bd6baedc2b05e8b90c2dae5ba0c7e343c2043c59"

// Existing databases must match exactly. There is no branch migration path.
func verifySchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var typ, name, table string
		var ddl sql.NullString
		if err := rows.Scan(&typ, &name, &table, &ddl); err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", typ, name, table, ddl.String)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != schemaFingerprint {
		return fmt.Errorf("database schema differs from this development build; use a fresh development database")
	}
	return nil
}
func (s *Store) ensureSchema(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return verifySchema(ctx, s.db)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, currentSchema); err != nil {
		return err
	}
	return tx.Commit()
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Open existing files read-only before opening a writer: rejecting another schema
// must not change its journal mode or run any DDL.
func openValidatedWriter(ctx context.Context, path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && info.Size() > 0 {
		u := url.URL{Scheme: "file", Path: path}
		q := url.Values{}
		q.Set("mode", "ro")
		u.RawQuery = q.Encode()
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			return nil, err
		}
		check := verifySchema(ctx, db)
		closeErr := db.Close()
		if err := errors.Join(check, closeErr); err != nil {
			return nil, err
		}
	}
	return sqlitex.OpenWriter(path)
}
