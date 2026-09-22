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

	"github.com/google/uuid"

	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/sqlitex"
)

//go:embed schema.sql
var schema string

// Store is a single product database, with serialized immediate transactions
// and independent bounded readers. Known schemas migrate atomically under the
// exclusive application/schema lock before workers or readers are opened.
type Store struct {
	release    func()
	closeOnce  sync.Once
	closeError error
	db, reader *sql.DB
	now        func() time.Time
	retention  retentionCursor
	writer     writeAdmission
	runtime    runtimeAdmission
	instance   string
}

func Open(ctx context.Context, path string) (result *Store, resultErr error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	release, err := schemastate.LockPaths(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			release()
		}
	}()
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
		check, err := sql.Open("sqlite", u.String())
		if err != nil {
			return nil, err
		}
		_, err = knownSchema(ctx, check)
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
	dsn := sqlitex.BuildDSN(path, true)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	if err = upgradeSchema(ctx, db, nil); err != nil {
		return fail(err)
	}
	if err = verifySchema(ctx, db); err != nil {
		return fail(err)
	}
	if err = sqlitex.VerifyPragmas(ctx, db, 1); err != nil {
		return fail(err)
	}
	instance := uuid.NewString()
	if _, err = db.ExecContext(ctx, `UPDATE application_release SET current_instance=? WHERE id=1`, instance); err != nil {
		return fail(err)
	}
	reader, err := sqlitex.OpenReader(ctx, path, 4)
	if err != nil {
		return fail(err)
	}
	return &Store{db: db, reader: reader, now: time.Now, release: release, instance: instance}, nil
}
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeError = errors.Join(s.reader.Close(), s.db.Close())
		if s.release != nil {
			s.release()
		}
	})
	return s.closeError
}
func (s *Store) timestamp() string { return sqlitex.FormatTime(s.now()) }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func schemaDigest(ctx context.Context, db queryer) (string, error) {
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

func verifySchema(ctx context.Context, db *sql.DB) error {
	version, err := knownSchema(ctx, db)
	if err == nil && version != len(schemaSteps) {
		return fmt.Errorf("product schema needs migration")
	}
	return err
}
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.writeClass(ctx, controlWrite, "", 1<<20, fn)
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
