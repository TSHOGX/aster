package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSchemaOwnershipMigrateAndRefuseNewer(t *testing.T) {
	name := filepath.Join(t.TempDir(), "metadata.db")
	ctx := context.Background()
	v1 := []string{"CREATE TABLE retained(id TEXT PRIMARY KEY,value TEXT NOT NULL);"}
	db, err := Open(ctx, name, v1)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(ctx, name, v1); err == nil {
		other.Close()
		t.Fatal("second owner accepted")
	}
	if _, err = db.Exec("INSERT INTO retained VALUES('one','history')"); err != nil {
		t.Fatal(err)
	}
	var journal string
	var sync, foreign int
	if err = db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("PRAGMA foreign_keys").Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || sync != 2 || foreign != 1 {
		t.Fatal("durability settings", journal, sync, foreign)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	v2 := append(append([]string(nil), v1...), "ALTER TABLE retained ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;")
	db, err = Open(ctx, name, v2)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	var rev int
	if err = db.QueryRow("SELECT value,revision FROM retained WHERE id='one'").Scan(&value, &rev); err != nil {
		t.Fatal(err)
	}
	if value != "history" || rev != 1 {
		t.Fatal("migration lost history")
	}
	db.Close()
	if old, err := Open(ctx, name, v1); err == nil {
		old.Close()
		t.Fatal("old schema writer accepted")
	}
	db, err = Open(ctx, name, v2)
	if err != nil {
		t.Fatal("refused writer damaged db", err)
	}
	db.Close()
}
func TestFailedMigrationRollsBack(t *testing.T) {
	name := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	if db, err := Open(ctx, name, []string{"CREATE TABLE example(id TEXT); INSERT INTO absent VALUES(1);"}); err == nil {
		db.Close()
		t.Fatal("invalid migration succeeded")
	}
	db, err := Open(ctx, name, []string{"CREATE TABLE example(id TEXT);"})
	if err != nil {
		t.Fatal("failed migration left partial schema", err)
	}
	db.Close()
}
