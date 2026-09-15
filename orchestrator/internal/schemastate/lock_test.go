package schemastate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalDatabaseLock(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "db")
	unlock, e := LockPaths(p)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if f, e := LockPaths(p); e == nil {
		f()
		t.Fatal("concurrent lock accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if e = os.Symlink(d, alias); e != nil {
		t.Fatal(e)
	}
	if f, e := LockPaths(filepath.Join(alias, "db")); e == nil {
		f()
		t.Fatal("symlink parent bypassed lock")
	}
}
func TestRefuseDatabaseAlias(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "db")
	if f, e := LockPaths(p, p); e == nil {
		f()
		t.Fatal("same database pair accepted")
	}
}
