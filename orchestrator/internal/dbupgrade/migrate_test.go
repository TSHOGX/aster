package dbupgrade

import (
	"context"
	"database/sql"
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func legacy(t *testing.T, base, kind, path string) {
	t.Helper()
	fixture := base + "-" + kind + ".sql"
	if kind == "memorystore" {
		fixture = "memorystore.sql"
	}
	b, e := os.ReadFile(filepath.Join("testdata", fixture))
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(string(b)); e != nil {
		t.Fatal(e)
	}
}
func TestBaselineUpgradeAndRepeat(t *testing.T) {
	for _, base := range []string{"669542d", "target-fresh", "target-upgraded", "0ccddb5"} {
		t.Run(base, func(t *testing.T) {
			dir := t.TempDir()
			c, m := filepath.Join(dir, "control.db"), filepath.Join(dir, "memory.db")
			legacy(t, base, "store", c)
			legacy(t, base, "memorystore", m)
			db, e := sql.Open("sqlite", c)
			if e != nil {
				t.Fatal(e)
			}
			_, e = db.Exec(`INSERT INTO users(id,display_name,created_at) VALUES('u','历史用户','2026-09-11'); INSERT INTO sessions(id,user_id,status,driver_id,mode,workspace,restore_id,created_at,updated_at) VALUES('s','u','created','pi','agent','/test','restore','2026-09-11','2026-09-11'); INSERT INTO messages(session_id,role,content,created_at) VALUES('s','user','历史消息','2026-09-11'); INSERT INTO turns(session_id,sequence,role,content,status,attempt,created_at) VALUES('s',1,'user','历史消息','queued',0,'2026-09-11')`)
			if e == nil && base == "0ccddb5" {
				_, e = db.Exec(`INSERT INTO sandbox_backends(name,store_id,version) VALUES('engine','retained-store','pinned'); INSERT INTO sandbox_environments(environment_id,session_id,backend_name,store_id,sandbox_id,desired,fence,mutation_revision,epoch) VALUES('retained','s','engine','retained-store','retained-sandbox','{}',4,'retained-revision',2)`)
			}
			db.Close()
			if e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			if e = Migrate(ctx, c, m, false, openFixture); e != nil {
				t.Fatal(e)
			}
			if e = Migrate(ctx, c, m, false, openFixture); e != nil {
				t.Fatal(e)
			}
			db, e = schemastate.ReadOnly(c)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			var text string
			var count, materializes int
			if e = db.QueryRow(`SELECT content FROM messages`).Scan(&text); e != nil || text != "历史消息" {
				t.Fatalf("message: %s %v", text, e)
			}
			if e = db.QueryRow(`SELECT count(*),max(materializes_message) FROM turns`).Scan(&count, &materializes); e != nil || count != 1 || materializes != 0 {
				t.Fatalf("turns: %d %d %v", count, materializes, e)
			}
			if base == "0ccddb5" {
				var state, sandbox string
				if e = db.QueryRow(`SELECT observed_state,sandbox_id FROM sandbox_environments WHERE environment_id='retained'`).Scan(&state, &sandbox); e != nil || state != "unknown" || sandbox != "retained-sandbox" {
					t.Fatalf("retained environment migration: %s %s %v", state, sandbox, e)
				}
			}
			var backend string
			wantBackend := "legacy"
			if base == "0ccddb5" {
				wantBackend = "khora"
			}
			if e = db.QueryRow(`SELECT backend FROM session_execution_backends WHERE session_id='s'`).Scan(&backend); e != nil || backend != wantBackend {
				t.Fatalf("migration changed session runtime ownership: %s want %s: %v", backend, wantBackend, e)
			}
		})
	}
}
func TestUnknownSchemaRefusesBoth(t *testing.T) {
	dir := t.TempDir()
	c, m := filepath.Join(dir, "control.db"), filepath.Join(dir, "memory.db")
	legacy(t, "669542d", "store", c)
	legacy(t, "669542d", "memorystore", m)
	db, _ := sql.Open("sqlite", m)
	db.Exec(`CREATE TABLE private_data(id INTEGER)`)
	db.Close()
	if e := Migrate(context.Background(), c, m, false, openFixture); e == nil {
		t.Fatal("accepted unknown schema")
	}
	s, e := schemastate.Check(context.Background(), c, "store")
	if e != nil || !s.Pending {
		t.Fatalf("control was modified: %+v %v", s, e)
	}
}
func TestMissingIsNotInitializedByPlan(t *testing.T) {
	dir := t.TempDir()
	c, m := filepath.Join(dir, "control.db"), filepath.Join(dir, "memory.db")
	if _, e := Plan(context.Background(), c, m); e == nil {
		t.Fatal("expected missing DB")
	}
	if _, e := os.Stat(c); !os.IsNotExist(e) {
		t.Fatal("plan created file")
	}
	if e := Migrate(context.Background(), c, m, true, openFixture); e != nil {
		t.Fatal(e)
	}
	if e := Migrate(context.Background(), c, m, true, openFixture); e == nil {
		t.Fatal("initialized existing pair")
	}
}
func TestPartialPairCanResume(t *testing.T) {
	d := t.TempDir()
	c, m := filepath.Join(d, "c.db"), filepath.Join(d, "m.db")
	legacy(t, "669542d", "store", c)
	legacy(t, "669542d", "memorystore", m)
	if e := one(context.Background(), c, "store", false, openFixture); e != nil {
		t.Fatal(e)
	}
	if e := Migrate(context.Background(), c, m, false, openFixture); e != nil {
		t.Fatal(e)
	}
}

func openFixture(ctx context.Context, path, kind string) (*sql.DB, func() error, error) {
	if kind == "store" {
		s, e := store.Open(ctx, path)
		if e != nil {
			return nil, nil, e
		}
		return s.DBForTest(), s.Close, nil
	}
	s, e := memorystore.Open(ctx, path)
	if e != nil {
		return nil, nil, e
	}
	return s.DBForTest(), s.Close, nil
}

func TestResumeInitializationAfterOneDatabase(t *testing.T) {
	d := t.TempDir()
	c, m := filepath.Join(d, "c.db"), filepath.Join(d, "m.db")
	if e := one(context.Background(), c, "store", true, openFixture); e != nil {
		t.Fatal(e)
	}
	if e := ResumeInitialize(context.Background(), c, m, openFixture); e != nil {
		t.Fatal(e)
	}
	if e := ResumeInitialize(context.Background(), c, m, openFixture); e != nil {
		t.Fatal(e)
	}
}
func TestManagedOpenDoesNotRewriteSchema(t *testing.T) {
	d := t.TempDir()
	c, m := filepath.Join(d, "c.db"), filepath.Join(d, "m.db")
	legacy(t, "669542d", "store", c)
	legacy(t, "669542d", "memorystore", m)
	ctx := context.Background()
	if s, e := store.OpenWithOptions(ctx, c, store.Options{Managed: true}); e == nil {
		s.Close()
		t.Fatal("managed open migrated legacy DB")
	}
	if e := Migrate(ctx, c, m, false, openFixture); e != nil {
		t.Fatal(e)
	}
	before, e := schemastate.Check(ctx, c, "store")
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.OpenWithOptions(ctx, c, store.Options{Managed: true})
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	after, e := schemastate.Check(ctx, c, "store")
	if e != nil || before.Fingerprint != after.Fingerprint {
		t.Fatalf("schema changed: %v", e)
	}
}

func TestStampedPreviousSchemaRequiresExplicitUpgrade(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	c, m := filepath.Join(dir, "control.db"), filepath.Join(dir, "memory.db")
	legacy(t, "target-fresh", "store", c)
	legacy(t, "target-fresh", "memorystore", m)
	for _, path := range []string{c, m} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		err = schemastate.Stamp(ctx, db)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := schemastate.Check(ctx, c, "store")
	if err != nil || !status.Pending {
		t.Fatalf("old stamped schema accepted: %+v %v", status, err)
	}
	if err := schemastate.Verify(ctx, c, "store"); err == nil {
		t.Fatal("old schema passed startup verification")
	}
	if err := Migrate(ctx, c, m, false, openFixture); err != nil {
		t.Fatal(err)
	}
	if err := schemastate.Verify(ctx, c, "store"); err != nil {
		t.Fatal(err)
	}
}
