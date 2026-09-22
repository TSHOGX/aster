package product

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"sync"
)

//go:embed display_schema.sql
var displaySchema string

//go:embed projector_schema.sql
var projectorSchema string

//go:embed event_frontier_schema.sql
var eventFrontierSchema string

//go:embed reader_schema.sql
var readerSchema string

//go:embed retention_schema.sql
var retentionSchema string

//go:embed navigation_schema.sql
var navigationSchema string

//go:embed release_schema.sql
var releaseSchema string

// Immutable, ordered schema steps. The original unversioned schema is recognized
// only by its complete digest. Unknown versions or DDL never enter a writer.
var schemaSteps = []string{schema, displaySchema, projectorSchema, eventFrontierSchema, readerSchema, retentionSchema, navigationSchema, releaseSchema}

var expectedSchemas = sync.OnceValues(func() ([]string, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	out := []string{}
	for _, step := range schemaSteps {
		if _, err = db.Exec(step); err != nil {
			return nil, err
		}
		digest, err := schemaDigest(context.Background(), db)
		if err != nil {
			return nil, err
		}
		out = append(out, digest)
	}
	return out, nil
})

func knownSchema(ctx context.Context, q queryer) (int, error) {
	expected, err := expectedSchemas()
	if err != nil {
		return 0, err
	}
	var version int
	if err = q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	actual, err := schemaDigest(ctx, q)
	if err != nil {
		return 0, err
	}
	if version == 0 && actual == expected[0] {
		return 1, nil
	}
	if version < 1 || version > len(expected) || actual != expected[version-1] {
		return 0, fmt.Errorf("unsupported or modified product schema (version %d); use a schema-compatible build", version)
	}
	return version, nil
}

func upgradeSchema(ctx context.Context, db *sql.DB, beforeCommit func() error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return err
	}
	version := 0
	if count > 0 {
		version, err = knownSchema(ctx, tx)
		if err != nil {
			return err
		}
	}
	for n := version; n < len(schemaSteps); n++ {
		if _, err = tx.ExecContext(ctx, schemaSteps[n]); err != nil {
			return fmt.Errorf("schema migration %d: %w", n+1, err)
		}
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", len(schemaSteps))); err != nil {
		return err
	}
	if version, err = knownSchema(ctx, tx); err != nil {
		return err
	}
	if version != len(schemaSteps) {
		return errors.New("schema migration did not reach target")
	}
	if beforeCommit != nil {
		if err = beforeCommit(); err != nil {
			return err
		}
	}
	return tx.Commit()
}
