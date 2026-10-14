// Package database opens a single-owner Studio database with exact schema
// verification and crash-durable SQLite writes. It imports no Aster packages.
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

type Database struct {
	*sql.DB
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

// Open creates or verifies the current schema under an exclusive lifetime lock.
// A mismatched schema is refused without changing it. Backups must include this DB,
// its companion blob store, and source identity under the application's GC pin.
func Open(ctx context.Context, name string, schema string) (*Database, error) {
	if !filepath.IsAbs(name) || schema == "" {
		return nil, fmt.Errorf("absolute database path and explicit schema required")
	}
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(name+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("database already owned: %w", err)
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		lock.Close()
		return nil, err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || !info.Mode().IsRegular() {
		lock.Close()
		return nil, fmt.Errorf("database must be a regular file")
	}
	uri := url.URL{Scheme: "file", Path: name}
	q := url.Values{"_pragma": {"foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)"}}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	result := &Database{DB: db, lock: lock}
	fail := func(e error) (*Database, error) { result.Close(); return nil, e }
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	fail = func(e error) (*Database, error) { _ = tx.Rollback(); result.Close(); return nil, e }
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		return fail(err)
	}
	if count == 0 && version == 0 {
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return fail(err)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion)); err != nil {
			return fail(err)
		}
	} else if version != SchemaVersion || count == 0 {
		return fail(fmt.Errorf("unsupported Studio schema %d", version))
	}
	expected, err := expectedSchemaDigest(schema)
	if err != nil {
		return fail(err)
	}
	actual, err := schemaDigest(ctx, tx)
	if err != nil {
		return fail(err)
	}
	if actual != expected {
		return fail(fmt.Errorf("unsupported Studio schema structure"))
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return fail(err)
	}
	err = errors.Join(dir.Sync(), dir.Close())
	if err != nil {
		return fail(err)
	}
	return result, nil
}
func (d *Database) Close() error {
	d.closeOnce.Do(func() { d.closeErr = errors.Join(d.DB.Close(), d.lock.Close()) })
	return d.closeErr
}

// Fingerprint SQLite's actual DDL, not a claimed version or stored digest.
func schemaDigest(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
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
func expectedSchemaDigest(schema string) (string, error) {
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
}
