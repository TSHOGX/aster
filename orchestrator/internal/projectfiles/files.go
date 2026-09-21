// Package projectfiles implements bounded workspace operations using directory
// descriptors. It has no knowledge of Sessions, turns or application locks.
package projectfiles

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
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
	After       string `json:"after,omitempty"`
	Limit       int    `json:"limit,omitempty"`
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
	if err := Path(r.Path, r.Operation == "list"); err != nil {
		return err
	}
	switch r.Operation {
	case "list":
		if r.Limit < 1 || r.Limit > MaxPage || (r.After != "" && (Path(r.After, false) != nil || strings.Contains(r.After, "/"))) {
			return ErrPath
		}
	case "read", "mkdir", "remove":
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
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}
type Page struct {
	Entries []Entry `json:"entries"`
	Next    string  `json:"next,omitempty"`
}

// A max heap retains just the next page, even for very large directories.
type names []string

func (n names) Len() int           { return len(n) }
func (n names) Less(i, j int) bool { return n[i] > n[j] }
func (n names) Swap(i, j int)      { n[i], n[j] = n[j], n[i] }
func (n *names) Push(x any)        { *n = append(*n, x.(string)) }
func (n *names) Pop() any          { old := *n; x := old[len(old)-1]; *n = old[:len(old)-1]; return x }

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

func Run(rootPath string, r Request, input io.Reader, output io.Writer) error {
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
	if r.Operation == "list" {
		return list(root, r, output)
	}
	fd, leaf, err := parent(root, r.Path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	switch r.Operation {
	case "read":
		f, err := unix.Openat(fd, leaf, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer unix.Close(f)
		if err = regular(f); err != nil {
			return err
		}
		// This procfs reference names the selected inode, not a second lookup of the
		// guest-controlled path. It also avoids opening FIFOs/devices before fstat.
		file, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", f))
		if err != nil {
			return err
		}
		defer file.Close()
		n, err := io.Copy(output, io.LimitReader(file, MaxTransfer+1))
		if err == nil && n > MaxTransfer {
			return errors.New("transfer exceeds bound")
		}
		return err

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
func list(root int, r Request, out io.Writer) error {
	fd, err := directory(root, r.Path)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), r.Path)
	defer dir.Close()
	kept := &names{}
	heap.Init(kept)
	for {
		batch, err := dir.Readdirnames(128)
		for _, name := range batch {
			if name <= r.After || !utf8.ValidString(name) {
				continue
			}
			heap.Push(kept, name)
			if kept.Len() > r.Limit+1 {
				heap.Pop(kept)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	sort.Strings(*kept)
	page := Page{Entries: []Entry{}}
	if len(*kept) > r.Limit {
		page.Next = (*kept)[r.Limit-1]
		*kept = (*kept)[:r.Limit]
	}
	for _, name := range *kept {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		kind := "other"
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			kind = "file"
		case unix.S_IFDIR:
			kind = "directory"
		case unix.S_IFLNK:
			kind = "symlink"
		}
		page.Entries = append(page.Entries, Entry{Name: name, Kind: kind, Size: st.Size, Modified: st.Mtim.Sec})
	}
	return json.NewEncoder(out).Encode(page)
}
