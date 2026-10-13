package product

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// SchemaVersion identifies the fresh schema lineage. Fingerprints, not version
// ordering, decide whether a database belongs to this executable.
const SchemaVersion = 9

var currentSchemaDigest = sync.OnceValues(func() (string, error) {
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

func SchemaIdentity() (int, string, error) {
	digest, err := currentSchemaDigest()
	return SchemaVersion, "sha256:" + digest, err
}

func verifySchema(ctx context.Context, q queryer) error {
	expected, err := currentSchemaDigest()
	if err != nil {
		return err
	}
	var version int
	if err = q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	actual, err := schemaDigest(ctx, q)
	if err != nil {
		return err
	}
	if version != SchemaVersion || actual != expected {
		return fmt.Errorf("unsupported product schema (version %d): this build requires a fresh database or its exact current schema; migrations are not supported", version)
	}
	return nil
}

func initializeSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion)); err != nil {
			return err
		}
	}
	if err = verifySchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
