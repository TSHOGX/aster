// Package shareblob is the content-addressed byte store behind public share
// links. A link's whitelisted files are copied in here when the link is created,
// so the bytes a link serves are frozen: an agent overwriting the file later, or
// the session being destroyed and its workspace reclaimed, cannot change or
// remove what an already-published URL returns.
//
// This is deliberately a LEAF package: stdlib only, no knowledge of store,
// server, sessions, or links. It does three things -- write bytes and report
// their digest, open bytes by digest, unlink a digest -- and it has no opinion
// about who references what. Reference counting lives with the table that holds
// the references (share_link_files), because that is the only place it can be
// decided transactionally. internal/architecture/layering_test.go pins the
// direction.
package shareblob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrTooLarge is returned when the incoming bytes exceed the caller's limit. The
// partial temp file is removed before returning, so a rejected write leaves
// nothing behind.
var ErrTooLarge = errors.New("share blob exceeds the size limit")

// ErrNotFound is returned when a digest has no stored bytes. A missing blob is
// expected, not exceptional: a link can outlive a botched reclamation, and the
// serving path must render that as a 404 rather than a 500.
var ErrNotFound = errors.New("share blob not found")

// Store holds blobs under a root directory.
type Store struct {
	root string
}

// New returns a Store rooted at dir, creating it if needed. The root holds only
// content-addressed files this package wrote; nothing else should be placed in
// it, because Remove trusts the layout.
func New(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("share blob root is required")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve share blob root: %w", err)
	}
	// 0o750: the orchestrator reads these to serve anonymous requests, but the
	// bytes are not world-readable on the host.
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("create share blob root: %w", err)
	}
	return &Store{root: absolute}, nil
}

// Root reports the directory the store writes into.
func (s *Store) Root() string {
	return s.root
}

// Put streams r into the store, capped at limit bytes, and returns the SHA-256
// hex digest and byte count.
//
// The write is atomic (temp file, fsync, rename) and idempotent by content: if
// the digest already exists the temp file is discarded and the existing blob is
// kept, so two links whitelisting identical bytes share one file. That dedup is
// exactly why reclamation has to be reference-counted rather than per-link.
func (s *Store) Put(r io.Reader, limit int64) (string, int64, error) {
	if limit <= 0 {
		return "", 0, fmt.Errorf("share blob limit must be positive")
	}
	temp, err := os.CreateTemp(s.root, ".incoming-*")
	if err != nil {
		return "", 0, fmt.Errorf("create share blob temp file: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}

	digest := sha256.New()
	// Read one byte past the limit so hitting it exactly is allowed while
	// exceeding it is detected without buffering the whole overflow.
	written, err := io.Copy(io.MultiWriter(temp, digest), io.LimitReader(r, limit+1))
	if err != nil {
		cleanup()
		return "", 0, fmt.Errorf("write share blob: %w", err)
	}
	if written > limit {
		cleanup()
		return "", 0, ErrTooLarge
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("sync share blob: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return "", 0, fmt.Errorf("close share blob: %w", err)
	}
	if err := os.Chmod(tempName, 0o640); err != nil {
		_ = os.Remove(tempName)
		return "", 0, fmt.Errorf("chmod share blob: %w", err)
	}

	sum := hex.EncodeToString(digest.Sum(nil))
	target, err := s.pathFor(sum)
	if err != nil {
		_ = os.Remove(tempName)
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		_ = os.Remove(tempName)
		return "", 0, fmt.Errorf("create share blob shard: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		// Same content already stored. Keep the existing file: an in-flight reader
		// may hold it open, and replacing it would gain nothing.
		_ = os.Remove(tempName)
		return sum, written, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tempName)
		return "", 0, fmt.Errorf("stat share blob: %w", err)
	}
	if err := os.Rename(tempName, target); err != nil {
		_ = os.Remove(tempName)
		return "", 0, fmt.Errorf("commit share blob: %w", err)
	}
	return sum, written, nil
}

// Open returns the stored bytes for a digest. The caller closes the file.
func (s *Store) Open(sum string) (*os.File, os.FileInfo, error) {
	path, err := s.pathFor(sum)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrNotFound
	}
	return file, info, nil
}

// Remove unlinks a digest's bytes. A digest that is already gone is not an
// error: reclamation must be safe to retry after a partial run.
//
// The caller is responsible for having established, inside the same transaction
// that dropped the last reference, that nothing else points at this digest.
func (s *Store) Remove(sum string) error {
	path, err := s.pathFor(sum)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// pathFor maps a digest to its file path, sharded by the first two hex
// characters so one directory does not accumulate every blob.
//
// The digest is validated as exactly 64 lowercase hex characters before it is
// joined to the root. That is what makes a digest safe to take from the
// database: no separator, no "..", nothing that could escape the root even if a
// row were somehow corrupted.
func (s *Store) pathFor(sum string) (string, error) {
	if len(sum) != sha256.Size*2 {
		return "", fmt.Errorf("invalid share blob digest length %d", len(sum))
	}
	for _, r := range sum {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return "", fmt.Errorf("invalid share blob digest")
		}
	}
	return filepath.Join(s.root, sum[:2], sum), nil
}
