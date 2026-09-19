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
	// BaselineSource records which catalog reviewed this fingerprint:
	// "embedded" for the one compiled into the binary, "local" for the
	// host-local file. Deployments carrying local entries are visible in
	// `db status` output without having to read the host's filesystem.
	BaselineSource string `json:"baseline_source,omitempty"`
	Version        int    `json:"version"`
	Pending        bool   `json:"pending"`
}

// BaselinesPathEnv names the optional host-local baseline file. It widens the
// embedded catalog for one deployment without editing a tracked file, so a
// machine whose schema carries local history can still run an unmodified
// release binary.
const BaselinesPathEnv = "HARNESS_SCHEMA_BASELINES_PATH"

type baselineEntry struct {
	label  string
	source string
}

// loadCatalog returns the embedded catalog widened by the optional file at
// BaselinesPathEnv.
//
// Embedded entries always win. A host-local file may only ADD fingerprints
// upstream does not know; it can never relabel one upstream has already
// reviewed. That asymmetry is what keeps this gate meaningful: if a local file
// could rename an upstream verdict, an operator could label a pre-migration
// fingerprint "target-…" and satisfy the HasPrefix check in Check, turning "this
// database has not been migrated" into a silent pass. Widening is safe because a
// fingerprint upstream has never seen carries no verdict to contradict.
func loadCatalog() (map[string]map[string]baselineEntry, error) {
	var embedded map[string]map[string]string
	if err := json.Unmarshal(baselineJSON, &embedded); err != nil {
		return nil, err
	}
	catalog := map[string]map[string]baselineEntry{}
	for kind, entries := range embedded {
		catalog[kind] = map[string]baselineEntry{}
		for fingerprint, label := range entries {
			catalog[kind][fingerprint] = baselineEntry{label: label, source: "embedded"}
		}
	}
	path := strings.TrimSpace(os.Getenv(BaselinesPathEnv))
	if path == "" {
		return catalog, nil
	}
	// A configured path that cannot be read or parsed is an error, not an empty
	// overlay: otherwise a typo in the unit file surfaces later as a refused
	// migration whose message points at the schema instead of at the typo.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w", BaselinesPathEnv, path, err)
	}
	var local map[string]map[string]string
	if err := json.Unmarshal(data, &local); err != nil {
		return nil, fmt.Errorf("parse %s %s: %w", BaselinesPathEnv, path, err)
	}
	for kind, entries := range local {
		if catalog[kind] == nil {
			catalog[kind] = map[string]baselineEntry{}
		}
		for fingerprint, label := range entries {
			if _, ok := catalog[kind][fingerprint]; ok {
				continue
			}
			catalog[kind][fingerprint] = baselineEntry{label: label, source: "local"}
		}
	}
	return catalog, nil
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
	catalog, err := loadCatalog()
	if err != nil {
		return s, err
	}
	entry, ok := catalog[kind][s.Fingerprint]
	if !ok {
		return s, fmt.Errorf("unrecognized %s schema %s: require a reviewed baseline, no writes performed", kind, s.Fingerprint)
	}
	s.Baseline = entry.label
	s.BaselineSource = entry.source
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
	if count != 1 || s.Version != Version || checksum != Checksum {
		return s, fmt.Errorf("unsupported or modified %s migration history", kind)
	}
	s.Pending = !strings.HasPrefix(s.Baseline, "target")
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
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS harness_schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,applied_at TEXT NOT NULL); INSERT INTO harness_schema_migrations VALUES(?,?,?) ON CONFLICT(version) DO UPDATE SET checksum=excluded.checksum,applied_at=excluded.applied_at`, Version, Checksum, time.Now().UTC().Format(time.RFC3339Nano))
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
