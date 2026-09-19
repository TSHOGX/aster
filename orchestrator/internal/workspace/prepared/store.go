// Package prepared retains upload bytes independently of a request or guest.
package prepared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"harness-platform/orchestrator/internal/workspace"
)

type Store struct{ root string }

func New(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute prepared upload root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, workspace.ErrForbidden
	}
	return &Store{root: root}, nil
}
func (s *Store) Prepare(ctx context.Context, source io.Reader, limit int64) (workspace.PreparedContent, error) {
	var result workspace.PreparedContent
	if s == nil || source == nil || limit < 0 || limit >= 1<<63-1 {
		return result, fmt.Errorf("prepared store, content and bounded size required")
	}
	file, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return result, err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, source}, limit+1))
	if err != nil {
		return result, err
	}
	if size > limit {
		return result, workspace.ErrTooLarge
	}
	if err := file.Chmod(0400); err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, err
	}
	result = workspace.PreparedContent{Digest: hex.EncodeToString(hash.Sum(nil)), Size: size}
	target := filepath.Join(s.root, result.Digest)
	if err := os.Link(file.Name(), target); err != nil {
		if !os.IsExist(err) {
			return result, err
		}
		existing, err := s.Open(ctx, result)
		if err != nil {
			return result, err
		}
		existing.Close()
	}
	root, err := os.Open(s.root)
	if err != nil {
		return result, err
	}
	defer root.Close()
	return result, root.Sync()
}
func (s *Store) Open(ctx context.Context, p workspace.PreparedContent) (io.ReadCloser, error) {
	if s == nil || len(p.Digest) != 64 || p.Size < 0 {
		return nil, fmt.Errorf("prepared content identity required")
	}
	if _, err := hex.DecodeString(p.Digest); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(s.root, p.Digest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	good := false
	defer func() {
		if !good {
			file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != p.Size {
		return nil, fmt.Errorf("prepared upload size changed")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx, file}); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != p.Digest {
		return nil, fmt.Errorf("prepared upload digest changed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	good = true
	return file, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
