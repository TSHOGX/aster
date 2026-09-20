package prepared

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const lockName = ".prepared.lock"

// Hold protects the prepare-to-durable-reference handoff across store instances
// and processes. Release only after the mutation commit, or preparation failure.
// Kernel locks vanish on process death; no timeout can revoke a living writer.
func (s *Store) Hold(ctx context.Context) (func(), error) { return s.lock(ctx, unix.LOCK_SH) }
func (s *Store) openDirectory() (*os.File, error) {
	directory, err := os.OpenFile(s.root, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := directory.Stat()
	if err != nil || !os.SameFile(s.identity, info) {
		directory.Close()
		return nil, fmt.Errorf("prepared content root changed")
	}
	return directory, nil
}
func (s *Store) lock(ctx context.Context, mode int) (func(), error) {
	if s == nil {
		return nil, fmt.Errorf("prepared content store required")
	}
	directory, err := s.openDirectory()
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), lockName, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), lockName)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("prepared content lock must be a regular file")
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), mode|unix.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() { once.Do(func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }) }, nil
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type References interface {
	AssertExecutionOwner(context.Context, string) error
	PreparedContentReferenced(context.Context, string, string) (bool, error)
}
type Collection struct {
	Scanned, Removed int
	Bytes            int64
}

// Collector visits a bounded directory page per step, retaining its directory
// cursor between steps. A new sweep rescans from the start: unlinking while
// enumerating may defer an entry until the next sweep, never lose its data.
type Collector struct {
	Store      *Store
	Repository References
	Owner      string
	mu         sync.Mutex
	directory  *os.File
	running    bool
}

func (c *Collector) Step(ctx context.Context, limit int) (Collection, error) {
	result := Collection{}
	if c.Store == nil || c.Repository == nil || c.Owner == "" || limit <= 0 || limit > 1024 {
		return result, fmt.Errorf("prepared collector ports, owner and bounded page required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.Store.lock(ctx, unix.LOCK_EX)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err := c.Repository.AssertExecutionOwner(ctx, c.Owner); err != nil {
		return result, err
	}
	if c.directory == nil {
		c.directory, err = c.Store.openDirectory()
		if err != nil {
			return result, err
		}
	}
	entries, err := c.directory.ReadDir(limit)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, err
	}
	if errors.Is(err, io.EOF) {
		defer func() { _ = c.directory.Close(); c.directory = nil }()
	}
	removed := false
	defer func() {
		if removed && c.directory != nil {
			_ = c.directory.Sync()
		}
	}()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Scanned++
		name := entry.Name()
		digest := len(name) == 64 && name == strings.ToLower(name)
		if digest {
			_, err := hex.DecodeString(name)
			digest = err == nil
		}
		temporary := strings.HasPrefix(name, ".upload-")
		if !digest && !temporary {
			continue
		}
		var info unix.Stat_t
		err := unix.Fstatat(int(c.directory.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return result, err
		}
		if info.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		// This owner check is in the same SQL transaction as the reference query.
		// The exclusive content lock prevents new reservations after this lookup.
		used, err := c.Repository.PreparedContentReferenced(ctx, c.Owner, name)
		if err != nil {
			return result, err
		}
		if used {
			continue
		}
		if err := unix.Unlinkat(int(c.directory.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return result, err
		}
		removed = true
		result.Removed++
		result.Bytes += info.Size
	}
	if removed {
		err = c.directory.Sync()
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directory != nil {
		err := c.directory.Close()
		c.directory = nil
		return err
	}
	return nil
}
func (c *Collector) Run(ctx context.Context, interval, timeout time.Duration, onError func(error)) error {
	if interval <= 0 || timeout <= 0 {
		return fmt.Errorf("positive prepared collection bounds required")
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("prepared collector already running")
	}
	c.running = true
	c.mu.Unlock()
	defer func() { _ = c.Close(); c.mu.Lock(); c.running = false; c.mu.Unlock() }()
	for ctx.Err() == nil {
		action, cancel := context.WithTimeout(ctx, timeout)
		_, err := c.Step(action, 128)
		cancel()
		if err != nil && onError != nil && !errors.Is(err, context.Canceled) {
			onError(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
