package projectfiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
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
	for _, path := range []string{"../secret", "/secret", "nested/../secret", "nested//leaf", "nested/./leaf", "bad\x00name", string([]byte{0xff}), strings.Repeat("a/", 65) + "x", "escape/secret", "nested/leaf", "pipe"} {
		var out bytes.Buffer
		if err := Run(root, Request{Operation: "read", Path: path}, nil, &out); err == nil || out.Len() > 0 {
			t.Fatalf("escaped %q: %q %v", path, out.String(), err)
		}
	}
	for _, op := range []string{"write", "mkdir", "remove", "move"} {
		r := Request{Operation: op, Path: "escape/secret", Destination: "x", Size: 1, Digest: digest([]byte("x"))}
		if err := Run(root, r, strings.NewReader("x"), io.Discard); err == nil {
			t.Fatalf("%s traversed symlink", op)
		}
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	must(err)
	if string(data) != "outside" {
		t.Fatal("outside file modified")
	}
	// Unlinking a symlink removes the link itself without following it.
	must(Run(root, Request{Operation: "remove", Path: "escape"}, nil, io.Discard))
}
func TestAtomicBoundedTransfersAndPages(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "data")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"short", strings.Repeat("x", 11)} {
		err := Run(root, Request{Operation: "write", Path: "data", Size: 10, Digest: digest([]byte("0123456789"))}, strings.NewReader(input), io.Discard)
		got, _ := os.ReadFile(target)
		if err == nil || string(got) != "original" {
			t.Fatal("partial replacement published", err, string(got))
		}
	}
	data := bytes.Repeat([]byte("世界\x00"), 1<<20)
	if err := Run(root, Request{Operation: "write", Path: "data", Size: int64(len(data)), Digest: digest(data)}, bytes.NewReader(data), io.Discard); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := Run(root, Request{Operation: "read", Path: "data"}, nil, &got); err != nil || !bytes.Equal(got.Bytes(), data) {
		t.Fatal("stream roundtrip", err)
	}
	for i := 0; i < 503; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for {
		got.Reset()
		if err := Run(root, Request{Operation: "list", Limit: 47, After: after}, nil, &got); err != nil {
			t.Fatal(err)
		}
		var p Page
		if err := json.Unmarshal(got.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Entries) > 47 {
			t.Fatal("unbounded page")
		}
		for _, e := range p.Entries {
			if seen[e.Name] {
				t.Fatal("duplicate", e.Name)
			}
			seen[e.Name] = true
		}
		if p.Next == "" {
			break
		}
		after = p.Next
	}
	if len(seen) != 504 {
		t.Fatal(len(seen))
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
		_ = Run(root, Request{Operation: "read", Path: "dir/value"}, nil, &out)
		if strings.Contains(out.String(), "SECRET") {
			t.Fatal("followed replaced symlink")
		}
		_ = Run(root, Request{Operation: "write", Path: "dir/value", Size: 4, Digest: digest([]byte("safe"))}, strings.NewReader("safe"), io.Discard)
	}
	got, _ := os.ReadFile(filepath.Join(outside, "value"))
	if string(got) != "SECRET" {
		t.Fatal("outside write")
	}
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
