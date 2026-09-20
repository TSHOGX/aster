package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentSchemaReopensAndExcludesRemovedSubsystems(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_execution_backends", "runtime_generation_resources", "network_profiles", "runtime_resource_instances", "sandbox_contracts", "session_driver_homes", "session_workspaces", "episode_memories", "semantic_memories", "evolution_runs", "raw_sources"} {
		var n int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("removed table %s: %d %v", table, n, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := verifySchema(ctx, st.db); err != nil {
		t.Fatal(err)
	}
}
func TestMismatchedDatabaseIsRejectedWithoutModification(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "foreign.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT); INSERT INTO sessions VALUES('production-synthetic')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(context.Context, string) (*Store, error){Open, OpenForMaintenance} {
		st, err := open(ctx, path)
		if err == nil {
			st.Close()
			t.Fatal("foreign schema accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(original) != sha256.Sum256(after) {
			t.Fatal("rejected database changed")
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
				t.Fatalf("rejected database created %s: %v", suffix, err)
			}
		}
	}
}
func TestSchemaDriftDoesNotTriggerRepair(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TRIGGER messages_fts_ai`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err := Open(ctx, path); err == nil {
		st.Close()
		t.Fatal("missing FTS trigger was silently repaired")
	}
}
