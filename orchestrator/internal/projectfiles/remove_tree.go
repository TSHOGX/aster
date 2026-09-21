package projectfiles

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// RemoveTree is an internal private-state reclamation primitive. Browser Files
// never calls it. Every directory is selected without following symlinks, and
// bounded traversal prevents an unbounded cleanup worker.
func RemoveTree(rootPath, name string) error {
	if err := Path(name, false); err != nil {
		return err
	}
	root, err := unix.Open(rootPath, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(root)
	fd, leaf, err := parent(root, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	remaining := 100000
	return removeEntry(fd, leaf, 0, &remaining)
}
func removeEntry(parent int, name string, depth int, remaining *int) error {
	*remaining--
	if depth > 128 || *remaining < 0 {
		return errors.New("private cleanup traversal bound exceeded")
	}
	fd, err := unix.Openat(parent, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		err = unix.Unlinkat(parent, name, 0)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	readable, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(readable), name)
	// Read bounded batches and restart after unlinking. Directory offsets are not
	// a snapshot and may skip entries after concurrent changes; a final rmdir
	// refuses to claim success while any entry remains.
	for {
		names, readErr := directory.Readdirnames(128)
		for _, child := range names {
			if err = removeEntry(readable, child, depth+1, remaining); err != nil {
				directory.Close()
				return err
			}
		}
		if readErr != nil {
			break
		}
	}
	directory.Close()
	err = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
