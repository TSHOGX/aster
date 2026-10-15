package projections

import (
	"aster/contracts/appidentity"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

type schemaReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func schemaDigest(ctx context.Context, q schemaReader) (string, error) {
	rows, err := q.QueryContext(ctx, "SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var kind, name, table, ddl string
		if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\n", kind, name, table, ddl)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), rows.Err()
}
func initializeSchema(ctx context.Context, db *sql.DB) error {
	expectedDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer expectedDB.Close()
	expectedDB.SetMaxOpenConns(1)
	if _, err = expectedDB.ExecContext(ctx, schema+evaluationSchema+workflowExecutionSchema+handoffSchema+generationGrantSchema+appidentity.SessionSchema); err != nil {
		return err
	}
	expected, err := schemaDigest(ctx, expectedDB)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count, version int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if count == 0 && version == 0 {
		if _, err = tx.ExecContext(ctx, schema+evaluationSchema+workflowExecutionSchema+handoffSchema+generationGrantSchema+appidentity.SessionSchema); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
			return err
		}
	} else if version != 1 {
		return fmt.Errorf("unsupported projection schema version")
	}
	actual, err := schemaDigest(ctx, tx)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("unsupported projection schema structure")
	}
	return tx.Commit()
}
