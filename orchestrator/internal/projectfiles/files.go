// Package projectfiles implements bounded workspace operations using directory
// descriptors. It has no knowledge of Sessions, turns or application locks.
package projectfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const MaxTransfer int64 = 256 << 20
const MaxPage = 200

var ErrPath = errors.New("invalid workspace path")
var ErrUncertain = errors.New("mutation applied but durability confirmation failed")

type Request struct {
	Operation   string `json:"operation"`
	Path        string `json:"path"`
	Destination string `json:"destination,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Digest      string `json:"digest,omitempty"`
}

func Path(name string, root bool) error {
	if name == "" && root {
		return nil
	}
	if len(name) == 0 || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\\") {
		return ErrPath
	}
	parts := strings.Split(name, "/")
	if len(parts) > 64 {
		return ErrPath
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return ErrPath
		}
	}
	return nil
}
func (r Request) Validate() error {
	if err := Path(r.Path, false); err != nil {
		return err
	}
	switch r.Operation {
	case "mkdir", "remove":
	case "write":
		digest, err := hex.DecodeString(r.Digest)
		if err != nil || len(digest) != 32 || strings.ToLower(r.Digest) != r.Digest {
			return errors.New("SHA-256 digest required")
		}
		if r.Size < 0 || r.Size > MaxTransfer {
			return errors.New("transfer exceeds bound")
		}
	case "move":
		if err := Path(r.Destination, false); err != nil {
			return err
		}
	default:
		return errors.New("unknown operation")
	}
	return nil
}

type Entry struct {
	ModifiedNano int64  `json:"modified_nano,string,omitempty"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Size         int64  `json:"size"`
	Modified     int64  `json:"modified"`
}
type Page struct {
	Snapshot string  `json:"snapshot,omitempty"`
	Entries  []Entry `json:"entries"`
	Next     string  `json:"next,omitempty"`
	Previous string  `json:"previous,omitempty"`
	Start    int     `json:"start"`
	Total    int     `json:"total"`
}

// DirectoryQuery seeks inside one server inventory, never by replaying client pages.
type DirectoryQuery struct {
	Path   string
	Cursor string
	Limit  int
	Name   string
	Seek   string // "after" or "around", empty for an ordinary cursor page.
}

func (q DirectoryQuery) Validate() error {
	if Path(q.Path, true) != nil || q.Limit < 1 || q.Limit > MaxPage || len(q.Cursor) > 4096 {
		return ErrPath
	}
	if q.Seek == "" {
		if q.Name != "" {
			return ErrPath
		}
	} else if q.Cursor != "" || (q.Seek != "after" && q.Seek != "around") ||
		q.Name == "" || len(q.Name) > 255 || strings.ContainsAny(q.Name, "/\\\x00") || q.Name == "." || q.Name == ".." {
		return ErrPath
	}
	return nil
}

// directory walks from a duplicated root FD. A substituted symlink is never
// followed, including at the leaf. Open descriptors retain Linux rename semantics.
func directory(root int, path string) (int, error) {
	fd, err := unix.Openat(root, ".", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if path != "" {
		for _, part := range strings.Split(path, "/") {
			next, e := unix.Openat(fd, part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if e != nil {
				return -1, e
			}
			var st unix.Stat_t
			if e = unix.Fstat(next, &st); e != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
				unix.Close(next)
				return -1, errors.New("directory required; symlinks are not followed")
			}
			fd = next
		}
	}
	// Reopen the already selected directory, not the original pathname. O_PATH
	// plus fstat is required by the pinned guest: its ordinary O_RDONLY openat
	// can follow an intermediate symlink even when O_NOFOLLOW is supplied.
	readable, e := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	unix.Close(fd)
	return readable, e
}
func parent(root int, path string) (int, string, error) {
	dir, leaf := "", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir, leaf = path[:i], path[i+1:]
	}
	fd, err := directory(root, dir)
	return fd, leaf, err
}
func regular(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("regular file required")
	}
	return nil
}

func Execute(rootPath string, r Request, input io.Reader) error {
	if err := r.Validate(); err != nil {
		return err
	}
	root, err := unix.Open(rootPath, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	var rootStat unix.Stat_t
	if err := unix.Fstat(root, &rootStat); err != nil {
		return err
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("workspace root must be a directory")
	}
	fd, leaf, err := parent(root, r.Path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	switch r.Operation {
	case "write":
		// A replacement does not open the target, so neither a target symlink nor a
		// concurrent substitution can redirect the write outside this directory.
		var st unix.Stat_t
		err := unix.Fstatat(fd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
			return errors.New("regular file target required")
		}
		if err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		temp := ".upload-" + uuid.NewString()
		f, err := unix.Openat(fd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(f), temp)
		defer file.Close()
		defer unix.Unlinkat(fd, temp, 0)
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(input, r.Size+1))
		if err != nil {
			return err
		}
		if n != r.Size {
			return fmt.Errorf("expected %d bytes, received %d", r.Size, n)
		}
		if hex.EncodeToString(hash.Sum(nil)) != r.Digest {
			return errors.New("upload digest mismatch")
		}
		if err = file.Sync(); err != nil {
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
		if err = unix.Renameat(fd, temp, fd, leaf); err != nil {
			return err
		}
		if err := unix.Fsync(fd); err != nil {
			return errors.Join(ErrUncertain, err)
		}
		return nil
	case "mkdir":
		return unix.Mkdirat(fd, leaf, 0755)
	case "remove":
		var st unix.Stat_t
		if err := unix.Fstatat(fd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		flags := 0
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			flags = unix.AT_REMOVEDIR
		}
		return unix.Unlinkat(fd, leaf, flags)
	case "move":
		dest, name, err := parent(root, r.Destination)
		if err != nil {
			return err
		}
		defer unix.Close(dest)
		return unix.Renameat(fd, leaf, dest, name)
	}
	return errors.New("unreachable operation")
}
