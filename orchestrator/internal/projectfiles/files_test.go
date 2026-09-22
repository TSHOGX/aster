package projectfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600))
	must(os.Symlink(outside, filepath.Join(root, "escape")))
	must(os.Mkdir(filepath.Join(root, "nested"), 0700))
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "nested", "leaf")))
	must(unix.Mkfifo(filepath.Join(root, "pipe"), 0600))
	for _, op := range []string{"write", "mkdir", "remove", "move"} {
		r := Request{Operation: op, Path: "escape/secret", Destination: "x", Size: 1, Digest: digest([]byte("x"))}
		if err := Execute(root, r, strings.NewReader("x")); err == nil {
			t.Fatalf("%s traversed symlink", op)
		}
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	must(err)
	if string(data) != "outside" {
		t.Fatal("outside file modified")
	}
	// Unlinking a symlink removes the link itself without following it.
	must(Execute(root, Request{Operation: "remove", Path: "escape"}, nil))
}
func TestAtomicBoundedTransfersAndPages(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "data")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"short", strings.Repeat("x", 11)} {
		err := Execute(root, Request{Operation: "write", Path: "data", Size: 10, Digest: digest([]byte("0123456789"))}, strings.NewReader(input))
		got, _ := os.ReadFile(target)
		if err == nil || string(got) != "original" {
			t.Fatal("partial replacement published", err, string(got))
		}
	}
	data := bytes.Repeat([]byte("世界\x00"), 1<<20)
	if err := Execute(root, Request{Operation: "write", Path: "data", Size: int64(len(data)), Digest: digest(data)}, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("atomic write roundtrip", err)
	}
}

func TestConcurrentSymlinkSubstitution(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "value"), []byte("SECRET"), 0600)
	os.Mkdir(filepath.Join(root, "dir"), 0700)
	os.WriteFile(filepath.Join(root, "dir", "value"), []byte("safe"), 0600)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "held"))
			os.Symlink(outside, filepath.Join(root, "dir"))
			os.Remove(filepath.Join(root, "dir"))
			os.Rename(filepath.Join(root, "held"), filepath.Join(root, "dir"))
		}
	}()
	defer func() { close(done); wg.Wait() }()
	for i := 0; i < 1000; i++ {
		var out bytes.Buffer
		reader, err := NewReader(root)
		if err != nil {
			t.Fatal(err)
		}
		_ = reader.Read(context.Background(), "dir/value", func(f ReadFrame) error { _, err := out.Write(f.Data); return err })
		reader.Close()
		if strings.Contains(out.String(), "SECRET") {
			t.Fatal("followed replaced symlink")
		}
		_ = Execute(root, Request{Operation: "write", Path: "dir/value", Size: 4, Digest: digest([]byte("safe"))}, strings.NewReader("safe"))
	}
	got, _ := os.ReadFile(filepath.Join(outside, "value"))
	if string(got) != "SECRET" {
		t.Fatal("outside write")
	}
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func TestPrivateCleanupNeverFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("sibling"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "session", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "session", "nested", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "session", "native"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(root, "session"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(root, "session"); err != nil {
		t.Fatal("cleanup retry", err)
	}
	if _, err := os.Stat(filepath.Join(root, "session")); !os.IsNotExist(err) {
		t.Fatal("private directory retained", err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(got) != "sibling" {
		t.Fatal("cleanup escaped root", err)
	}
}
