package local

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/workspace"
)

// Directory is the local backend binding. Host paths and ownership never cross
// the workspace operation port or appear in returned file metadata.
type Directory struct {
	Path     string
	UID, GID int
}

func (d Directory) Replace(ctx context.Context, name string, source io.Reader, maxBytes int64) (workspace.File, error) {
	return d.replace(ctx, name, source, maxBytes, nil)
}

// Descriptor-relative operations pin every parent before creating or replacing
// a name. They remain confined when a guest swaps a path component for a symlink.
// No openat2 dependency is needed in guests that lack that syscall.
func (d Directory) replace(ctx context.Context, name string, source io.Reader, maxBytes int64, hook func(string) error) (workspace.File, error) {
	result := workspace.File{Path: name}
	clean, err := workspace.CleanPath(name)
	if err != nil {
		return result, err
	}
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return result, workspace.ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := unix.Open(d.Path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return result, classifyPathError(err)
	}
	defer unix.Close(root)
	parts := strings.Split(clean, "/")
	parent, err := d.parent(root, parts[:len(parts)-1])
	if err != nil {
		return result, err
	}
	defer unix.Close(parent)
	if hook != nil {
		if err := hook("parent-open"); err != nil {
			return result, err
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, err
	}
	staging := fmt.Sprintf(".upload-%x", nonce)
	fd, err := unix.Openat(parent, staging, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return result, err
	}
	file := os.NewFile(uintptr(fd), staging)
	defer file.Close()
	defer unix.Unlinkat(parent, staging, 0)
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(contextReader{ctx, source}, maxBytes+1))
	if err != nil {
		return result, err
	}
	if written > maxBytes {
		return result, workspace.ErrTooLarge
	}
	if err := setOwnership(fd, d.UID, d.GID); err != nil {
		return result, err
	}
	if err := file.Chmod(0o640); err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, err
	}
	result.Size = written
	result.Digest = hex.EncodeToString(digest.Sum(nil))
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	result.ModifiedAt = info.ModTime().UTC()
	if hook != nil {
		if err := hook("file-synced"); err != nil {
			return result, err
		}
	}
	target := parts[len(parts)-1]
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, target, &stat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return result, workspace.ErrForbidden
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Renaming replaces the final directory entry; it never follows a symlink
	// substituted after Fstatat. Concurrent guest modification is not file CAS.
	if err := unix.Renameat(parent, staging, parent, target); err != nil {
		return result, err
	}
	if hook != nil {
		if err := hook("renamed"); err != nil {
			return result, fmt.Errorf("%w: %v", workspace.ErrUncertain, err)
		}
	}
	if err := unix.Fsync(parent); err != nil {
		return result, fmt.Errorf("%w: %v", workspace.ErrUncertain, err)
	}
	return result, nil
}

func (d Directory) parent(root int, parts []string) (int, error) {
	fd, err := unix.Openat(root, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.ENOENT) {
			if err = unix.Mkdirat(fd, part, 0o750); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return -1, err
			}
			next, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err == nil {
				err = setOwnership(next, d.UID, d.GID)
				if err == nil {
					err = unix.Fchmod(next, 0o750)
				}
				if err == nil {
					err = unix.Fsync(next)
				}
				if err == nil {
					err = unix.Fsync(fd)
				}
				if err != nil {
					unix.Close(next)
				}
			}
		}
		unix.Close(fd)
		if err != nil {
			return -1, classifyPathError(err)
		}
		fd = next
	}
	return fd, nil
}

func setOwnership(fd, uid, gid int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if int(stat.Uid) == uid && int(stat.Gid) == gid {
		return nil
	}
	return unix.Fchown(fd, uid, gid)
}
func classifyPathError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("%w: symlink or non-directory component", workspace.ErrForbidden)
	}
	return err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
