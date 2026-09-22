package product

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestIncrementalMigrationFromRecentSchemasIsAtomic(t *testing.T) {
	for _, previous := range []int{6, 7} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "previous.db")
			db := baselineDatabase(t, path)
			for _, step := range schemaSteps[1:previous] {
				_, err := db.Exec(step)
				must(t, err)
			}
			_, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", previous))
			must(t, err)
			before, err := knownSchema(ctx, db)
			must(t, err)
			injected := errors.New("interrupt incremental migration")
			if err = upgradeSchema(ctx, db, func() error { return injected }); !errors.Is(err, injected) {
				t.Fatal(err)
			}
			after, err := knownSchema(ctx, db)
			must(t, err)
			if before != after {
				t.Fatal("migration partially committed", before, after)
			}
			must(t, db.Close())
			s, err := Open(ctx, path)
			must(t, err)
			defer s.Close()
			version, err := knownSchema(ctx, s.reader)
			must(t, err)
			if version != len(schemaSteps) {
				t.Fatal(version)
			}
			turns, err := s.Turns(ctx, Scope{"owner", "p"}, "s", 0, 100)
			must(t, err)
			if len(turns) != 1 || turns[0].RequestID != "original-request" {
				t.Fatal("lost authoritative turn", turns)
			}
		})
	}
}

func baselineDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	must(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(schema)
	must(t, err)
	s := &Store{db: db, reader: db, now: time.Now}
	_, err = s.CreateProject(ctx, "owner", "p", "retained project")
	must(t, err)
	_, err = s.CreateSession(ctx, Scope{"owner", "p"}, "s", "retained session")
	must(t, err)
	_, err = s.AdmitTurn(ctx, Scope{"owner", "p"}, "s", "original-request", "retained prompt", false)
	must(t, err)
	return db
}

func TestBaselineMigrationBackupAndCompatibleReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	db := baselineDatabase(t, path)
	backup := filepath.Join(t.TempDir(), "backup.db")
	_, err := db.Exec("VACUUM INTO ?", backup)
	must(t, err)
	must(t, db.Close())
	s, err := Open(ctx, path)
	must(t, err)
	scope := Scope{"owner", "p"}
	turns, err := s.Turns(ctx, scope, "s", 0, 100)
	must(t, err)
	if len(turns) != 1 || turns[0].RequestID != "original-request" || turns[0].Content != "retained prompt" {
		t.Fatal(turns)
	}
	_, err = s.AdmitTurn(ctx, scope, "s", "post-upgrade", "accepted after upgrade", true)
	must(t, err)
	must(t, s.Close())
	// A schema-compatible rollback build/reader must observe post-upgrade writes.
	s, err = Open(ctx, path)
	must(t, err)
	turns, err = s.Turns(ctx, scope, "s", 0, 100)
	must(t, err)
	if len(turns) != 2 {
		t.Fatal("lost accepted write", turns)
	}
	must(t, s.Close())
	// Rehearse restoring the pre-upgrade backup to a separate disposable path.
	restored, err := Open(ctx, backup)
	must(t, err)
	defer restored.Close()
	turns, err = restored.Turns(ctx, scope, "s", 0, 100)
	must(t, err)
	if len(turns) != 1 {
		t.Fatal("backup not an independent pre-upgrade snapshot", turns)
	}
}

func TestInterruptedMigrationRollsBackDDLAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	db := baselineDatabase(t, path)
	before, err := schemaDigest(ctx, db)
	must(t, err)
	injected := errors.New("interrupted before commit")
	if err = upgradeSchema(ctx, db, func() error { return injected }); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	after, err := schemaDigest(ctx, db)
	must(t, err)
	var version int
	must(t, db.QueryRow("PRAGMA user_version").Scan(&version))
	if before != after || version != 0 {
		t.Fatalf("partial migration persisted: %s %s %d", before, after, version)
	}
	must(t, db.Close())
	s, err := Open(ctx, path)
	must(t, err)
	defer s.Close()
	must(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != len(schemaSteps) {
		t.Fatal(version)
	}
}

func TestUnknownSchemaVersionRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	db := baselineDatabase(t, path)
	_, err := db.Exec("PRAGMA user_version=999")
	must(t, err)
	must(t, db.Close())
	if s, err := Open(ctx, path); err == nil {
		s.Close()
		t.Fatal("accepted unknown version")
	}
}
