// Package projections retains immutable central configuration projections.
// It does not own group configuration or infer activation from receipt arrival.
package projections

import (
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"aster/contracts/evolution"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("projection_conflict")
var ErrNotFound = errors.New("projection_not_found")
var ErrCatalog = errors.New("configuration_catalog_invalid")

type Store struct {
	db        *sql.DB
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}
type Projection struct {
	OperationID   string                  `json:"operation_id"`
	Digest        string                  `json:"digest"`
	Configuration evolution.Configuration `json:"configuration"`
}

const schema = `CREATE TABLE IF NOT EXISTS projections(group_id TEXT NOT NULL,revision INTEGER NOT NULL,digest TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE,body BLOB NOT NULL,PRIMARY KEY(group_id,revision));
CREATE TRIGGER IF NOT EXISTS immutable_projection BEFORE UPDATE ON projections BEGIN SELECT RAISE(ABORT,'immutable configuration projection'); END;
CREATE TRIGGER IF NOT EXISTS retained_projection BEFORE DELETE ON projections BEGIN SELECT RAISE(ABORT,'retain configuration provenance'); END;
CREATE TABLE workflow_revisions(id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),digest TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(id,revision));
CREATE TRIGGER immutable_workflow BEFORE UPDATE ON workflow_revisions BEGIN SELECT RAISE(ABORT,'immutable workflow revision'); END;
CREATE TRIGGER retained_workflow BEFORE DELETE ON workflow_revisions BEGIN SELECT RAISE(ABORT,'retain workflow provenance'); END;
CREATE TABLE catalog_definitions(kind TEXT NOT NULL CHECK(kind IN ('service','template','profile')),id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),digest TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(kind,id,revision));
CREATE TRIGGER immutable_definition BEFORE UPDATE ON catalog_definitions BEGIN SELECT RAISE(ABORT,'immutable catalog definition'); END;
CREATE TRIGGER retained_definition BEFORE DELETE ON catalog_definitions BEGIN SELECT RAISE(ABORT,'retain catalog provenance'); END;
CREATE TABLE service_states(service_id TEXT PRIMARY KEY,revision INTEGER NOT NULL CHECK(revision>0),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)));
CREATE TABLE service_state_receipts(request_id TEXT PRIMARY KEY,request TEXT NOT NULL,revision INTEGER NOT NULL);
CREATE TABLE runs(worker_id TEXT NOT NULL DEFAULT '',fence INTEGER NOT NULL DEFAULT 0 CHECK(fence>=0),lease_until INTEGER NOT NULL DEFAULT 0,id TEXT PRIMARY KEY,group_id TEXT NOT NULL,binding_id TEXT NOT NULL,configuration_revision INTEGER NOT NULL,occurrence_id TEXT NOT NULL,request BLOB NOT NULL,body BLOB NOT NULL,state TEXT NOT NULL CHECK(state IN ('queued','running','paused','completed','failed','cancelled')),UNIQUE(group_id,binding_id,configuration_revision,occurrence_id));
CREATE UNIQUE INDEX one_active_binding_run ON runs(group_id,binding_id) WHERE state IN ('queued','running','paused');
CREATE TRIGGER immutable_run_admission BEFORE UPDATE OF id,group_id,binding_id,configuration_revision,occurrence_id,request,body ON runs BEGIN SELECT RAISE(ABORT,'immutable run admission'); END;
CREATE TRIGGER retained_run BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT,'retain run provenance'); END;`

func Open(ctx context.Context, path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute projection database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("projection store already owned: %w", err)
	}
	opened := false
	defer func() {
		if !opened {
			lock.Close()
		}
	}()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("projection database must be regular")
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_pragma": {"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)"}}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = initializeSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		db.Close()
		return nil, err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		db.Close()
		return nil, err
	}
	opened = true
	return &Store{db: db, lock: lock}, nil
}
func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closeErr = errors.Join(s.db.Close(), s.lock.Close()) })
	return s.closeErr
}

// Adopt is an internal participant command. Its caller must authenticate central
// service authority and validate catalog/resource grants before invoking it.
func (s *Store) Adopt(ctx context.Context, p Projection) error {
	if !manifest.ValidID(p.OperationID) {
		return fmt.Errorf("operation identity required")
	}
	if err := p.Configuration.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p.Configuration)
	if err != nil {
		return err
	}
	if evolution.Digest(raw) != p.Digest {
		return fmt.Errorf("projection digest mismatch")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old []byte
	var operation, digest string
	err = tx.QueryRowContext(ctx, "SELECT operation_id,digest,body FROM projections WHERE group_id=? AND revision=?", p.Configuration.GroupID, p.Configuration.Revision).Scan(&operation, &digest, &old)
	if err == nil {
		if operation != p.OperationID || digest != p.Digest || !bytes.Equal(old, raw) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = validateConfigurationCatalog(ctx, tx, p.Configuration); err != nil {
		return fmt.Errorf("%w: %v", ErrCatalog, err)
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM projections WHERE operation_id=?", p.OperationID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO projections VALUES(?,?,?,?,?)", p.Configuration.GroupID, p.Configuration.Revision, p.Digest, p.OperationID, raw); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Read(ctx context.Context, group string, revision int64) (Projection, error) {
	var p Projection
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT operation_id,digest,body FROM projections WHERE group_id=? AND revision=?", group, revision).Scan(&p.OperationID, &p.Digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if evolution.Digest(raw) != p.Digest {
		return p, fmt.Errorf("projection integrity mismatch")
	}
	p.Configuration, err = evolution.Decode[evolution.Configuration](bytes.NewReader(raw))
	if err != nil {
		return p, err
	}
	if p.Configuration.GroupID != group || p.Configuration.Revision != revision {
		return p, fmt.Errorf("projection identity mismatch")
	}
	return p, p.Configuration.Validate()
}
