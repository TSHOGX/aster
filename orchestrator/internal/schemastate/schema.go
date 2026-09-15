// Package schemastate verifies schemas without running application migrations.
package schemastate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"strings"
	"time"
)

const Version = 1
const Checksum = "harness-schema-adoption-v1-copy-verify-replace"

//go:embed baselines.json
var baselineJSON []byte

type Status struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Baseline    string `json:"baseline"`
	Version     int    `json:"version"`
	Pending     bool   `json:"pending"`
}

func ReadOnly(path string) (*sql.DB, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("database is not a regular file: %s", path)
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Fingerprints include table constraints, indexes and triggers. Unknown DDL is
// deliberately refused, rather than reconstructed and silently discarded.
func Fingerprint(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name != 'harness_schema_migrations' ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var typ, name, table string
		var ddl sql.NullString
		if err := rows.Scan(&typ, &name, &table, &ddl); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", typ, name, table, ddl.String)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Check(ctx context.Context, path, kind string) (Status, error) {
	s := Status{Kind: kind, Path: path}
	db, err := ReadOnly(path)
	if err != nil {
		return s, err
	}
	defer db.Close()
	s.Fingerprint, err = Fingerprint(ctx, db)
	if err != nil {
		return s, err
	}
	var catalog map[string]map[string]string
	if err = json.Unmarshal(baselineJSON, &catalog); err != nil {
		return s, err
	}
	s.Baseline = catalog[kind][s.Fingerprint]
	if s.Baseline == "" {
		return s, fmt.Errorf("unrecognized %s schema %s: require a reviewed baseline, no writes performed", kind, s.Fingerprint)
	}
	var exists int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='harness_schema_migrations' AND type='table'`).Scan(&exists); err != nil {
		return s, err
	}
	if exists == 0 {
		s.Pending = true
		return s, nil
	}
	var checksum string
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*),coalesce(max(version),0),coalesce(max(checksum),'') FROM harness_schema_migrations`).Scan(&count, &s.Version, &checksum); err != nil {
		return s, err
	}
	if count != 1 || s.Version != Version || checksum != Checksum || !strings.HasPrefix(s.Baseline, "target") {
		return s, fmt.Errorf("unsupported or modified %s migration history", kind)
	}
	return s, nil
}
func Verify(ctx context.Context, path, kind string) error {
	s, err := Check(ctx, path, kind)
	if err != nil {
		return err
	}
	if s.Pending {
		return fmt.Errorf("%s requires explicit db migrate", kind)
	}
	return nil
}
func Stamp(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE harness_schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,applied_at TEXT NOT NULL); INSERT INTO harness_schema_migrations VALUES(?,?,?)`, Version, Checksum, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func Integrity(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check failed: %s", result)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign_key_check failed")
	}
	return rows.Err()
}
