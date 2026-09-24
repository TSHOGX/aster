package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var ctx = context.Background()

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func wantError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
func fixture(t *testing.T) (*Store, Scope, Scope) {
	t.Helper()
	s := testStore(t)
	a := Scope{"alice", "a"}
	b := Scope{"bob", "b"}
	_, err := s.CreateProject(ctx, a.Actor, a.ProjectID, "Alpha")
	must(t, err)
	_, err = s.CreateProject(ctx, b.Actor, b.ProjectID, "Beta")
	must(t, err)
	return s, a, b
}

func TestProjectChildrenAndImmutableOwnership(t *testing.T) {
	s, a, b := fixture(t)
	sessions, err := s.Sessions(ctx, a, "", 100)
	must(t, err)
	if len(sessions) != 0 {
		t.Fatal(sessions)
	}
	for i := 0; i < 3; i++ {
		_, err := s.CreateSession(ctx, a, fmt.Sprintf("s%d", i), "Agent")
		must(t, err)
		_, err = s.CreateTerminal(ctx, a, fmt.Sprintf("t%d", i), "Terminal", 24, 80)
		must(t, err)
	}
	sessions, err = s.Sessions(ctx, a, "", 100)
	must(t, err)
	terms, err := s.Terminals(ctx, a, "", 100)
	must(t, err)
	if len(sessions) != 3 || len(terms) != 3 {
		t.Fatal(sessions, terms)
	}
	for _, scope := range []Scope{b, {"bob", "a"}, {"alice", "b"}, {"", "a"}} {
		_, err := s.Session(ctx, scope, "s0")
		wantError(t, err, ErrNotFound)
		_, err = s.Terminal(ctx, scope, "t0")
		wantError(t, err, ErrNotFound)
		_, err = s.AdmitTurn(ctx, scope, "s0", "r", "hello", false)
		wantError(t, err, ErrNotFound)
		wantError(t, s.RenameSession(ctx, scope, "s0", 1, "x"), ErrNotFound)
		wantError(t, s.DeleteTerminal(ctx, scope, "t0", 1), ErrNotFound)
	}
	for _, statement := range []string{
		`UPDATE sessions SET project_id='b' WHERE id='s0'`,
		`UPDATE terminals SET project_id='b' WHERE id='t0'`,
		`INSERT INTO events(project_id,session_id,revision,kind,created_at) VALUES('b','s0',1,'invalid','now')`,
		`INSERT INTO events(project_id,terminal_id,revision,kind,created_at) VALUES('b','t0',1,'invalid','now')`,
		`INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,state,sequence,created_at,updated_at) VALUES('bad','b','s0','bad','bad',0,'queued',1,'now','now')`,
	} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatalf("ownership violation accepted: %s", statement)
		}
	}
	// SQLite itself does not encode browser selection or the retired shell model.
	for _, table := range []string{"projects", "sessions", "terminals"} {
		var ddl string
		must(t, s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, table).Scan(&ddl))
		for _, legacy := range []string{"workspace", "sandbox_id", "driver_id", "mode ", "active_generation"} {
			if strings.Contains(ddl, legacy) {
				t.Fatalf("legacy %s in %s", legacy, table)
			}
		}
	}
}

func TestIndependentRevisionsAndAtomicOutbox(t *testing.T) {
	s, a, _ := fixture(t)
	child, err := s.CreateSession(ctx, a, "s", "Agent")
	must(t, err)
	terminal, err := s.CreateTerminal(ctx, a, "t", "Terminal", 24, 80)
	must(t, err)
	before, err := s.Project(ctx, a)
	must(t, err)
	_, err = s.AdmitTurn(ctx, a, child.ID, "request", "hello", false)
	must(t, err)
	after, err := s.Project(ctx, a)
	must(t, err)
	if after.Revision != before.Revision || after.ActivityRevision != before.ActivityRevision+1 {
		t.Fatal(before, after)
	}
	untouched, err := s.Terminal(ctx, a, terminal.ID)
	must(t, err)
	if untouched.Revision != terminal.Revision {
		t.Fatal(untouched)
	}
	must(t, s.RenameTerminal(ctx, a, terminal.ID, terminal.Revision, "New terminal"))
	wantError(t, s.RenameTerminal(ctx, a, terminal.ID, terminal.Revision, "stale"), ErrConflict)
	wantError(t, s.RenameSession(ctx, a, child.ID, child.Revision, "stale"), ErrConflict)
	must(t, s.RenameProject(ctx, a, before.Revision, "New project"))
	wantError(t, s.RenameProject(ctx, a, before.Revision, "stale"), ErrConflict)
	events, err := s.Events(ctx, a, 0, 500)
	must(t, err)
	// project create + (session create,parent) + (terminal create,parent) +
	// turn admission + terminal rename + project rename. Failed CAS emits none.
	if len(events) != 8 {
		t.Fatalf("outbox: %+v", events)
	}
	for i, e := range events {
		if e.ProjectID != a.ProjectID || i > 0 && e.ID <= events[i-1].ID {
			t.Fatal(events)
		}
	}
	first, err := s.Events(ctx, a, 0, 2)
	must(t, err)
	next, err := s.Events(ctx, a, first[1].ID, 2)
	must(t, err)
	if len(first) != 2 || len(next) != 2 || next[0].ID != events[2].ID {
		t.Fatal(first, next)
	}
}

func TestHeldAdmissionAndIdempotencyAcrossRestartAndRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	s, err := Open(ctx, path)
	must(t, err)
	a := Scope{"alice", "a"}
	_, err = s.CreateProject(ctx, a.Actor, a.ProjectID, "A")
	must(t, err)
	child, err := s.CreateSession(ctx, a, "s", "Agent")
	must(t, err)
	before, err := s.Project(ctx, a)
	must(t, err)
	held, err := s.AdmitTurn(ctx, a, child.ID, "request", "hello", true)
	must(t, err)
	p, err := s.Project(ctx, a)
	must(t, err)
	if p.ActivityRevision != before.ActivityRevision || held.State != "held" {
		t.Fatal(p, held)
	}
	child, err = s.Session(ctx, a, child.ID)
	must(t, err)
	must(t, mutationErr(s, a, child.ID, held, "release-held", "release"))
	p, err = s.Project(ctx, a)
	must(t, err)
	if p.ActivityRevision != before.ActivityRevision+1 {
		t.Fatal(p)
	}
	child, err = s.Session(ctx, a, child.ID)
	must(t, err)
	must(t, s.RenameSession(ctx, a, child.ID, child.Revision, "Renamed"))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	duplicate, err := s.AdmitTurn(ctx, a, child.ID, "request", "hello", true)
	must(t, err)
	if duplicate.ID != held.ID || duplicate.State != "queued" {
		t.Fatal(duplicate)
	}
	_, err = s.AdmitTurn(ctx, a, child.ID, "request", "changed", true)
	wantError(t, err, ErrConflict)
	_, err = s.AdmitTurn(ctx, a, child.ID, "request", "hello", false)
	wantError(t, err, ErrConflict)
	replayed, err := s.CreateSession(ctx, a, child.ID, "Agent")
	must(t, err)
	if replayed.Title != "Renamed" {
		t.Fatal(replayed)
	}
	turns, err := s.Turns(ctx, a, child.ID, 0, 500)
	must(t, err)
	if len(turns) != 1 {
		t.Fatal(turns)
	}
	var synchronous int
	must(t, s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous))
	if synchronous != 2 {
		t.Fatalf("durability synchronous=%d, want FULL (2)", synchronous)
	}
}

func TestConcurrentAdmissionDeduplicatesAndOrders(t *testing.T) {
	s, a, _ := fixture(t)
	_, err := s.CreateSession(ctx, a, "s", "Agent")
	must(t, err)
	var wg sync.WaitGroup
	type admission struct {
		id  string
		err error
	}
	errs := make(chan admission, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("request-%d", i%20)
			_, err := s.AdmitTurn(ctx, a, "s", id, "hello", false)
			errs <- admission{id: id, err: err}
		}(i)
	}
	wg.Wait()
	close(errs)
	for result := range errs {
		// Capacity is an explicit rejection before transaction admission. Retry
		// that same intent only after the bounded burst has drained; unknown
		// outcomes are not eligible for this retry.
		if errors.Is(result.err, ErrCapacity) {
			_, result.err = s.AdmitTurn(ctx, a, "s", result.id, "hello", false)
		}
		must(t, result.err)
	}
	turns, err := s.Turns(ctx, a, "s", 0, 500)
	must(t, err)
	if len(turns) != 20 {
		t.Fatal(len(turns))
	}
	for i, tr := range turns {
		if tr.Sequence != int64(i+1) {
			t.Fatal(turns)
		}
	}
	p, err := s.Project(ctx, a)
	must(t, err)
	if p.ActivityRevision != 20 {
		t.Fatal(p)
	}
}

func TestNarrowShareAndDeletionAdmissionFence(t *testing.T) {
	s, a, _ := fixture(t)
	child, err := s.CreateSession(ctx, a, "s", "Agent")
	must(t, err)
	_, err = s.CreateTerminal(ctx, a, "t", "Terminal", 24, 80)
	must(t, err)
	must(t, s.GrantConversation(ctx, a, child.ID, "bob"))
	must(t, s.CanReadConversation(ctx, "bob", child.ID))
	narrow := Scope{"bob", a.ProjectID}
	_, err = s.Project(ctx, narrow)
	wantError(t, err, ErrNotFound)
	_, err = s.Events(ctx, narrow, 0, 100)
	wantError(t, err, ErrNotFound)
	_, err = s.Terminal(ctx, narrow, "t")
	wantError(t, err, ErrNotFound)
	_, err = s.AdmitTurn(ctx, narrow, child.ID, "r", "attack", false)
	wantError(t, err, ErrNotFound)
	must(t, s.RevokeConversation(ctx, a, child.ID, "bob"))
	wantError(t, s.CanReadConversation(ctx, "bob", child.ID), ErrNotFound)
	must(t, s.GrantConversation(ctx, a, child.ID, "bob"))
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, s.DeleteProject(ctx, a, p.Revision))
	wantError(t, s.CanReadConversation(ctx, "bob", child.ID), ErrNotFound)
	_, err = s.CreateSession(ctx, a, "new", "Agent")
	wantError(t, err, ErrDeleting)
	_, err = s.CreateTerminal(ctx, a, "new", "Terminal", 24, 80)
	wantError(t, err, ErrDeleting)
	_, err = s.AdmitTurn(ctx, a, child.ID, "r", "new", false)
	wantError(t, err, ErrDeleting)
	p, err = s.Project(ctx, a)
	must(t, err)
	if p.State != "deleting" {
		t.Fatal(p)
	}
	// Cleanup is an intent. S01 must not pretend remote destruction happened.
	child, err = s.Session(ctx, a, child.ID)
	must(t, err)
	if child.State != "idle" {
		t.Fatal(child)
	}
}

func TestChildDeletionLeavesProjectAndSiblingUntouched(t *testing.T) {
	s, a, _ := fixture(t)
	child, err := s.CreateSession(ctx, a, "s", "Agent")
	must(t, err)
	_, err = s.CreateSession(ctx, a, "sibling", "Agent")
	must(t, err)
	term, err := s.CreateTerminal(ctx, a, "t", "Terminal", 24, 80)
	must(t, err)
	must(t, s.GrantConversation(ctx, a, child.ID, "reader"))
	child, err = s.Session(ctx, a, child.ID)
	must(t, err)
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, s.DeleteSession(ctx, a, child.ID, child.Revision))
	wantError(t, s.CanReadConversation(ctx, "reader", child.ID), ErrNotFound)
	_, err = s.AdmitTurn(ctx, a, child.ID, "r", "hello", false)
	wantError(t, err, ErrDeleting)
	sibling, err := s.Session(ctx, a, "sibling")
	must(t, err)
	if sibling.State != "idle" || sibling.Revision != 1 {
		t.Fatal(sibling)
	}
	after, err := s.Project(ctx, a)
	must(t, err)
	if p.State != after.State || p.Revision != after.Revision {
		t.Fatal(p, after)
	}
	current, err := s.Terminal(ctx, a, term.ID)
	must(t, err)
	if current != term {
		t.Fatal(current, term)
	}
}

func TestRejectForeignSchemaAndDriftWithoutModification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	db, err := sql.Open("sqlite", path)
	must(t, err)
	_, err = db.Exec(`CREATE TABLE sessions(id TEXT); INSERT INTO sessions VALUES('do-not-touch')`)
	must(t, err)
	must(t, db.Close())
	original, err := os.ReadFile(path)
	must(t, err)
	if st, err := Open(ctx, path); err == nil {
		st.Close()
		t.Fatal("foreign database accepted")
	}
	after, err := os.ReadFile(path)
	must(t, err)
	if sha256.Sum256(original) != sha256.Sum256(after) {
		t.Fatal("foreign database modified")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("foreign database sidecar %s: %v", suffix, err)
		}
	}
	path = filepath.Join(t.TempDir(), "current.db")
	st, err := Open(ctx, path)
	must(t, err)
	_, err = st.db.Exec(`DROP TRIGGER sessions_parent_immutable`)
	must(t, err)
	must(t, st.Close())
	if st, err := Open(ctx, path); err == nil {
		st.Close()
		t.Fatal("schema drift repaired silently")
	}
}

func TestProductDatabaseHasOneApplicationOwnerAcrossPathAliases(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "product.db")
	first, err := Open(ctx, path)
	must(t, err)
	if second, err := Open(ctx, path); err == nil {
		second.Close()
		t.Fatal("second application owner accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	must(t, os.Symlink(directory, alias))
	if second, err := Open(ctx, filepath.Join(alias, "product.db")); err == nil {
		second.Close()
		t.Fatal("path alias bypassed owner lock")
	}
	must(t, first.Close())
	must(t, first.Close())
	second, err := Open(ctx, path)
	must(t, err)
	must(t, second.Close())
}
