package authoring_test

import (
	"context"
	"database/sql"
	_ "modernc.org/sqlite"
	"os"
	"testing"
)

func TestProductionSchemaExcludesLibraryDraftTable(t *testing.T) {
	// This package imports the test-linked authoring package, whose historical
	// fixtures extend its schema. Use the production DDL directly for this check.
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	var legacy int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='draft_entries'").Scan(&legacy); err != nil || legacy != 0 {
		t.Fatal("legacy draft schema retained", legacy, err)
	}
	for _, table := range []string{"branches", "branch_entries", "prepared_merges", "merge_reviews", "merge_operations"} {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing current schema", table, err)
		}
	}
}
