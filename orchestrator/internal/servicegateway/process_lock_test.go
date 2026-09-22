package servicegateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayLifetimeLockFencesAliasesAndReplacement(t *testing.T) {
	directory := t.TempDir()
	unlock, err := LockProcess(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	alias := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, alias} {
		if other, err := LockProcess(path); err == nil {
			other()
			t.Fatal("concurrent gateway accepted", path)
		}
	}
	unlock()
	unlock()
	replacement, err := LockProcess(alias)
	if err != nil {
		t.Fatal(err)
	}
	replacement()
	if err = os.Remove(filepath.Join(directory, "gateway.lock")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(directory, "gateway.lock")); err != nil {
		t.Fatal(err)
	}
	if other, err := LockProcess(directory); err == nil {
		other()
		t.Fatal("symlink lock accepted")
	}
}
