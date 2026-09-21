package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/sqlitex"
)

//go:embed schema.sql
var schema string

// Store is a single product database, with serialized immediate transactions
// and independent bounded readers. There is no migration or legacy fallback.
type Store struct {
	db, reader *sql.DB
	now        func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
		check, err := sql.Open("sqlite", u.String())
		if err != nil {
			return nil, err
		}
		err = verifySchema(ctx, check)
		closeErr := check.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Commands acknowledged from this database must survive an application crash.
	// FULL is intentional: runner journal reclamation must not rely on NORMAL's
	// weaker power-loss contract. Disk/controller guarantees remain external.
	dsn := strings.Replace(sqlitex.BuildDSN(path, true), "synchronous%28NORMAL%29", "synchronous%28FULL%29", 1)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err == nil && count == 0 {
		_, err = tx.ExecContext(ctx, schema)
	}
	if err != nil {
		tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	if err = verifySchema(ctx, db); err != nil {
		return fail(err)
	}
	if err = sqlitex.VerifyPragmas(ctx, db, 1); err != nil {
		return fail(err)
	}
	reader, err := sqlitex.OpenReader(ctx, path, 4)
	if err != nil {
		return fail(err)
	}
	return &Store{db: db, reader: reader, now: time.Now}, nil
}
func (s *Store) Close() error      { return errors.Join(s.reader.Close(), s.db.Close()) }
func (s *Store) timestamp() string { return sqlitex.FormatTime(s.now()) }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func schemaDigest(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
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
	return fmt.Sprintf("%x", h.Sum(nil)), rows.Err()
}

var expectedSchema = sync.OnceValues(func() (string, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		return "", err
	}
	return schemaDigest(context.Background(), db)
})

func verifySchema(ctx context.Context, db *sql.DB) error {
	expected, err := expectedSchema()
	if err != nil {
		return err
	}
	actual, err := schemaDigest(ctx, db)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("database schema differs from this build; use a fresh development database")
	}
	return nil
}
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsRune(s, 0)
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
