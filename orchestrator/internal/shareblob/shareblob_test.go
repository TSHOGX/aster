package shareblob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestPutOpenRoundTrip(t *testing.T) {
	store := newTestStore(t)
	content := "<html><body>chart</body></html>"

	sum, size, err := store.Put(strings.NewReader(content), 1<<20)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("expected size %d, got %d", len(content), size)
	}
	expected := sha256.Sum256([]byte(content))
	if sum != hex.EncodeToString(expected[:]) {
		t.Fatalf("digest mismatch: %s", sum)
	}

	file, info, err := store.Open(sum)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	if info.Size() != size {
		t.Fatalf("stat size %d, want %d", info.Size(), size)
	}
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content mismatch: %q", got)
	}
}

// Identical bytes must collapse to one file. That dedup is the reason
// reclamation has to be reference-counted rather than per-link.
func TestPutDeduplicatesIdenticalContent(t *testing.T) {
	store := newTestStore(t)
	first, _, err := store.Put(strings.NewReader("same bytes"), 1<<20)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, _, err := store.Put(strings.NewReader("same bytes"), 1<<20)
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if first != second {
		t.Fatalf("expected the same digest, got %s and %s", first, second)
	}
	count := 0
	if err := filepath.Walk(store.Root(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one stored file, found %d", count)
	}
}

// A rejected write must leave nothing behind, including no temp file.
func TestPutRejectsOversizedAndLeavesNothing(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.Put(strings.NewReader("0123456789"), 4)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	entries, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected an empty root after a rejected write, found %v", entries)
	}

	// A payload exactly at the limit is accepted.
	if _, size, err := store.Put(strings.NewReader("0123"), 4); err != nil || size != 4 {
		t.Fatalf("expected an exact-limit write to succeed, got size=%d err=%v", size, err)
	}
}

func TestOpenMissingDigestIsNotFound(t *testing.T) {
	store := newTestStore(t)
	missing := strings.Repeat("a", 64)
	if _, _, err := store.Open(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	sum, _, err := store.Put(strings.NewReader("bytes"), 1<<20)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Remove(sum); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	// Reclamation must be safe to retry after a partial run.
	if err := store.Remove(sum); err != nil {
		t.Fatalf("second remove should be a no-op, got %v", err)
	}
	if _, _, err := store.Open(sum); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the blob to be gone, got %v", err)
	}
}

// A digest is taken from a database row, so a malformed one must never be joined
// to the root as-is.
func TestDigestValidationRejectsTraversal(t *testing.T) {
	store := newTestStore(t)
	bad := []string{
		"",
		"short",
		"../../etc/passwd",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64), // uppercase is not the canonical encoding
		strings.Repeat("z", 64), // not hex
		"a/" + strings.Repeat("a", 62),
	}
	for _, digest := range bad {
		if _, _, err := store.Open(digest); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("Open(%q) must fail validation, got %v", digest, err)
		}
		if err := store.Remove(digest); err == nil {
			t.Fatalf("Remove(%q) must fail validation", digest)
		}
	}
}

func TestNewRequiresRoot(t *testing.T) {
	if _, err := New("  "); err == nil {
		t.Fatal("expected an empty root to be rejected")
	}
}
