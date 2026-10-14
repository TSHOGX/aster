package database

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const testSchema = "CREATE TABLE retained(id TEXT PRIMARY KEY,value TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1);"

func TestSchemaOwnershipPersistenceAndDurability(t *testing.T) {
	name := filepath.Join(t.TempDir(), "metadata.db")
	ctx := context.Background()
	db, err := Open(ctx, name, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(ctx, name, testSchema); err == nil {
		other.Close()
		t.Fatal("second owner accepted")
	}
	if _, err = db.Exec("INSERT INTO retained(id,value) VALUES('one','history')"); err != nil {
		t.Fatal(err)
	}
	var journal string
	var sync, foreign, version int
	for query, dest := range map[string]any{"PRAGMA journal_mode": &journal, "PRAGMA synchronous": &sync, "PRAGMA foreign_keys": &foreign, "PRAGMA user_version": &version} {
		if err = db.QueryRow(query).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if journal != "wal" || sync != 2 || foreign != 1 || version != 1 {
		t.Fatal("durability/schema settings", journal, sync, foreign, version)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, name, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	var revision int
	if err = db.QueryRow("SELECT value,revision FROM retained WHERE id='one'").Scan(&value, &revision); err != nil {
		t.Fatal(err)
	}
	if value != "history" || revision != 1 {
		t.Fatal("reopen lost data", value, revision)
	}
}

func TestMismatchedSchemaRefusedWithoutChangingDatabase(t *testing.T) {
	for _, change := range []string{"PRAGMA user_version=2", "PRAGMA user_version=0", "ALTER TABLE retained ADD COLUMN extra TEXT", "CREATE INDEX extra ON retained(value)"} {
		t.Run(change, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "state.db")
			ctx := context.Background()
			db, err := Open(ctx, name, testSchema)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(change); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if db, err = Open(ctx, name, testSchema); err == nil {
				db.Close()
				t.Fatal("mismatched schema accepted")
			}
			after, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed database")
			}
		})
	}
}

func TestFailedInitializationRollsBack(t *testing.T) {
	name := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	if db, err := Open(ctx, name, "CREATE TABLE example(id TEXT); INSERT INTO absent VALUES(1);"); err == nil {
		db.Close()
		t.Fatal("invalid schema succeeded")
	}
	db, err := Open(ctx, name, "CREATE TABLE example(id TEXT);")
	if err != nil {
		t.Fatal("failed initialization left partial schema", err)
	}
	db.Close()
}

func TestEmptyDatabaseWithUnknownVersionRefused(t *testing.T) {
	name := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if opened, err := Open(context.Background(), name, testSchema); err == nil {
		opened.Close()
		t.Fatal("versioned empty database accepted")
	}
	db, err = sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != 99 || count != 0 {
		t.Fatal(fmt.Sprint(version, count))
	}
}
