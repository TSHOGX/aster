// Package database opens a single-owner Studio database with explicit schema
// evolution and crash-durable SQLite writes. It imports no Aster packages.
package database

import (
	"context"
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

type Database struct {
	*sql.DB
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

// Open applies ordered additive migrations under an exclusive lifetime lock.
// A newer schema is refused without changing it. Backups must include this DB,
// its companion blob store, and source identity under the application's GC pin.
func Open(ctx context.Context, name string, migrations []string) (*Database, error) {
	if !filepath.IsAbs(name) || len(migrations) == 0 {
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
	if version < 0 || version > len(migrations) {
		return fail(fmt.Errorf("unsupported Studio schema %d", version))
	}
	if version == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return fail(err)
		}
		if count != 0 {
			return fail(fmt.Errorf("unversioned nonempty database refused"))
		}
	}
	for i := version; i < len(migrations); i++ {
		if _, err = tx.ExecContext(ctx, migrations[i]); err != nil {
			return fail(err)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			return fail(err)
		}
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
