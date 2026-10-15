package authoring_test

import (
	"context"
	"database/sql"
	_ "modernc.org/sqlite"
	"os"
	"testing"
)

func TestProductionSchemaExcludesLibraryDraftTable(t *testing.T) {
	// Execute the production DDL directly to assert obsolete storage is absent.
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
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('libraries') WHERE name IN ('draft_revision','base_version')").Scan(&legacy); err != nil || legacy != 0 {
		t.Fatal("legacy library draft metadata retained", legacy, err)
	}
	for _, table := range []string{"branches", "branch_entries", "prepared_merges", "merge_reviews", "merge_operations"} {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing current schema", table, err)
		}
	}
}
