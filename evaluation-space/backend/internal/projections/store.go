// Package projections retains immutable central configuration projections.
// It does not own group configuration or infer activation from receipt arrival.
package projections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	"aster/contracts/evolution"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("projection_conflict")
var ErrNotFound = errors.New("projection_not_found")

type Store struct{ db *sql.DB }
type Projection struct {
	OperationID   string                  `json:"operation_id"`
	Digest        string                  `json:"digest"`
	Configuration evolution.Configuration `json:"configuration"`
}

const schema = `CREATE TABLE IF NOT EXISTS projections(group_id TEXT NOT NULL,revision INTEGER NOT NULL,digest TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE,body BLOB NOT NULL,PRIMARY KEY(group_id,revision));
CREATE TRIGGER IF NOT EXISTS immutable_projection BEFORE UPDATE ON projections BEGIN SELECT RAISE(ABORT,'immutable configuration projection'); END;
CREATE TRIGGER IF NOT EXISTS retained_projection BEFORE DELETE ON projections BEGIN SELECT RAISE(ABORT,'retain configuration provenance'); END;`

func Open(ctx context.Context, path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute projection database path required")
	}
	uri := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_pragma": {"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)"}}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

// Adopt is an internal participant command. Its caller must authenticate central
// service authority and validate catalog/resource grants before invoking it.
func (s *Store) Adopt(ctx context.Context, p Projection) error {
	if p.OperationID == "" || len(p.OperationID) > 128 {
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
