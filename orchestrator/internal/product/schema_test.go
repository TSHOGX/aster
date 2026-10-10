package product

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentSchemaInitializesAndReopensWithoutConversion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	s, err := openFixtureStore(ctx, path)
	must(t, err)
	_, err = s.CreateProject(ctx, "owner", "project", "Current project")
	must(t, err)
	_, err = s.CreateSession(ctx, Scope{"owner", "project"}, "session", "Current session")
	must(t, err)
	_, err = s.AdmitTurn(ctx, Scope{"owner", "project"}, "session", "request", "Current prompt", true)
	must(t, err)
	before, err := schemaDigest(ctx, s.reader)
	must(t, err)
	must(t, s.Close())
	s, err = openFixtureStore(ctx, path)
	must(t, err)
	defer s.Close()
	after, err := schemaDigest(ctx, s.reader)
	must(t, err)
	if before != after {
		t.Fatal("schema changed during reopen")
	}
	rows, err := s.Turns(ctx, Scope{"owner", "project"}, "session", 0, 100)
	must(t, err)
	if len(rows) != 1 || rows[0].RequestID != "request" {
		t.Fatal(rows)
	}
	var obsolete int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='workspace_reader_retirements'`).Scan(&obsolete))
	if obsolete != 0 {
		t.Fatal("migration-only cleanup table remains")
	}
}
func TestHistoricalAndUnknownSchemaVersionsAreRefusedWithoutWrites(t *testing.T) {
	for _, version := range []int{0, 1, 12, 13, 14, 15, 999} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "old.db")
			s, err := openFixtureStore(ctx, path)
			must(t, err)
			must(t, s.Close())
			db, err := sql.Open("sqlite", path)
			must(t, err)
			_, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version))
			must(t, err)
			must(t, db.Close())
			before, err := os.ReadFile(path)
			must(t, err)
			if opened, err := openFixtureStore(ctx, path); err == nil {
				opened.Close()
				t.Fatal("historical version accepted")
			}
			after, err := os.ReadFile(path)
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("refused schema changed")
			}
		})
	}
}
