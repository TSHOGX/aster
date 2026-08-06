package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// Every snapshot must be traversable by the unprivileged sandbox uid. /agents is
// bind-mounted read-only into each sandbox and read as a non-root user, so a
// snapshot root of 0700 makes the entire knowledge base unreadable even when
// every file inside is 0644 — the failure looks like "permission denied on
// /agents", not like a mode bug.
//
// This regressed once for a structural reason worth remembering: os.MkdirTemp
// creates 0700 and os.Rename preserves it, so every pipeline-written snapshot was
// 0700. It went unnoticed because `current` had only ever pointed at a
// boot-seeded snapshot, which copyTree creates 0755.
func TestSnapshotDirsAreSandboxTraversable(t *testing.T) {
	root := t.TempDir()
	kb := NewKB(root)
	mems, skills := sampleKBContent("perm check")

	digest, err := kb.Publish(mems, skills)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	assertTraversable(t, kb.SnapshotDir(digest))

	// The reseed path writes snapshots too, and must not reintroduce this.
	seed := writeSeedTree(t, map[string]string{"skills/doris-query/SKILL.md": "---\nstatus: in-use\n---\nseeded\n"})
	res, err := kb.Reseed(seed, nil, 0)
	if err != nil {
		t.Fatalf("Reseed: %v", err)
	}
	if !res.Changed {
		t.Fatal("expected the reseed to stage a new snapshot")
	}
	assertTraversable(t, kb.SnapshotDir(res.Digest))
}

// assertTraversable checks that every directory in the tree carries o+rx and
// every file o+r, which is what an unprivileged reader needs.
func assertTraversable(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if mode := info.Mode().Perm(); mode&0o055 != 0o055 {
		t.Errorf("snapshot root %s is %#o; the sandbox uid cannot traverse it", dir, mode)
	}
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		mode := fi.Mode().Perm()
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			if mode&0o055 != 0o055 {
				t.Errorf("dir %s is %#o, want o+rx", rel, mode)
			}
			return nil
		}
		if mode&0o004 == 0 {
			t.Errorf("file %s is %#o, want o+r", rel, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}
