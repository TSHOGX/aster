package projectfiles

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var ErrReaderCapacity = errors.New("workspace read capacity exhausted")
var ErrSnapshotExpired = errors.New("directory snapshot expired")
var ErrChangedDuringRead = errors.New("file changed during read")

const MaxInventoryNames = 100000
const MaxSnapshotBytes = 32 << 20
const MaxReaderSnapshotBytes = 64 << 20
const MaxReaderSnapshots = 16
const SnapshotLifetime = 30 * time.Second
const ReaderIdleTimeout = 30 * time.Second
const ReadOperationTimeout = 2 * time.Minute

type directorySnapshot struct {
	id, path string
	entries  []Entry
	bytes    int
	expires  time.Time
}
type directoryCursor struct {
	Snapshot string `json:"s"`
	Index    int    `json:"i"`
}

// Reader owns a confined root descriptor and bounded immutable directory
// observations. It never holds a lifecycle activity lease or mutates a file.
type Reader struct {
	root          int
	mu            sync.Mutex
	snapshots     map[string]*directorySnapshot
	snapshotBytes int
	scan          chan struct{}
	now           func() time.Time
	close         sync.Once
}

func NewReader(rootPath string) (*Reader, error) {
	root, err := unix.Open(rootPath, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(root, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		unix.Close(root)
		return nil, ErrPath
	}
	return &Reader{root: root, snapshots: map[string]*directorySnapshot{}, scan: make(chan struct{}, 1), now: time.Now}, nil
}
func (r *Reader) Close() error {
	var err error
	r.close.Do(func() { err = unix.Close(r.root); r.mu.Lock(); r.snapshots = nil; r.snapshotBytes = 0; r.mu.Unlock() })
	return err
}
func (r *Reader) Stats() (snapshots, bytes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.snapshots), r.snapshotBytes
}
func (r *Reader) open(path string) (*os.File, error) {
	if Path(path, false) != nil {
		return nil, ErrPath
	}
	parentFD, leaf, err := parent(r.root, path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, leaf, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	if err = regular(fd); err != nil {
		return nil, err
	}
	return os.Open(fmt.Sprintf("/proc/self/fd/%d", fd))
}
func metadata(st unix.Stat_t) ReadMetadata {
	kind := "other"
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = "file"
	case unix.S_IFDIR:
		kind = "directory"
	case unix.S_IFLNK:
		kind = "symlink"
	}
	return ReadMetadata{Size: st.Size, ModifiedNano: st.Mtim.Nano(), Kind: kind}
}
func (r *Reader) Stat(path string) (ReadMetadata, error) {
	file, err := r.open(path)
	if err != nil {
		return ReadMetadata{}, err
	}
	defer file.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &st); err != nil {
		return ReadMetadata{}, err
	}
	return metadata(st), nil
}
func (r *Reader) Read(ctx context.Context, path string, send func(ReadFrame) error) error {
	file, err := r.open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var before unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &before); err != nil {
		return err
	}
	if before.Size > MaxTransfer {
		return ErrReaderCapacity
	}
	info := metadata(before)
	if err = send(ReadFrame{Kind: "metadata", Metadata: &info}); err != nil {
		return err
	}
	hash := sha256.New()
	buffer := make([]byte, ReaderChunkBytes)
	var size int64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, e := file.Read(buffer)
		if n > 0 {
			if size+int64(n) > MaxTransfer {
				return ErrReaderCapacity
			}
			if err = send(ReadFrame{Kind: "data", Offset: size, Data: buffer[:n]}); err != nil {
				return err
			}
			_, _ = hash.Write(buffer[:n])
			size += int64(n)
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	var after unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &after); err != nil {
		return err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || size != after.Size {
		return ErrChangedDuringRead
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The digest identifies captured bytes. Stat comparisons detect many races,
	// but do not promise an atomic historical state of a concurrently edited file.
	return send(ReadFrame{Kind: "complete", Size: size, Digest: hex.EncodeToString(hash.Sum(nil))})
}
func (r *Reader) inventory(ctx context.Context, path string) ([]Entry, int, error) {
	fd, err := directory(r.root, path)
	if err != nil {
		return nil, 0, err
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	entries := []Entry{}
	names, bytes := 0, 0
	for {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		batch, e := dir.Readdirnames(128)
		names += len(batch)
		if names > MaxInventoryNames {
			return nil, 0, ErrReaderCapacity
		}
		for _, name := range batch {
			if !utf8.ValidString(name) {
				continue
			}
			var st unix.Stat_t
			if err = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, unix.ENOENT) {
					continue
				}
				return nil, 0, err
			}
			info := metadata(st)
			entry := Entry{Name: name, Kind: info.Kind, Size: st.Size, Modified: st.Mtim.Sec, ModifiedNano: info.ModifiedNano}
			// Includes string storage, Go object and encoded metadata overhead.
			bytes += len(name)*2 + 160
			if bytes > MaxSnapshotBytes {
				return nil, 0, ErrReaderCapacity
			}
			entries = append(entries, entry)
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, 0, e
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, bytes, nil
}
func (r *Reader) expire(now time.Time) {
	for id, snapshot := range r.snapshots {
		if !snapshot.expires.After(now) {
			delete(r.snapshots, id)
			r.snapshotBytes -= snapshot.bytes
		}
	}
}
func (r *Reader) List(ctx context.Context, path, cursor string, limit int) (Page, error) {
	if Path(path, true) != nil || limit < 1 || limit > MaxPage || len(cursor) > 2048 {
		return Page{}, ErrPath
	}
	var current *directorySnapshot
	index := 0
	if cursor == "" {
		// Directory enumeration has one slot; content/stat work remains independent.
		select {
		case r.scan <- struct{}{}:
		case <-ctx.Done():
			return Page{}, ctx.Err()
		}
		entries, bytes, err := r.inventory(ctx, path)
		<-r.scan
		if err != nil {
			return Page{}, err
		}
		current = &directorySnapshot{id: uuid.NewString(), path: path, entries: entries, bytes: bytes, expires: r.now().Add(SnapshotLifetime)}
		r.mu.Lock()
		r.expire(r.now())
		for len(r.snapshots) >= MaxReaderSnapshots || r.snapshotBytes+bytes > MaxReaderSnapshotBytes {
			var oldest *directorySnapshot
			for _, snapshot := range r.snapshots {
				if oldest == nil || snapshot.expires.Before(oldest.expires) {
					oldest = snapshot
				}
			}
			if oldest == nil {
				r.mu.Unlock()
				return Page{}, ErrReaderCapacity
			}
			delete(r.snapshots, oldest.id)
			r.snapshotBytes -= oldest.bytes
		}
		r.snapshots[current.id] = current
		r.snapshotBytes += bytes
		r.mu.Unlock()
	} else {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return Page{}, ErrPath
		}
		var token directoryCursor
		if json.Unmarshal(raw, &token) != nil || token.Index < 0 {
			return Page{}, ErrPath
		}
		r.mu.Lock()
		r.expire(r.now())
		current = r.snapshots[token.Snapshot]
		r.mu.Unlock()
		if current == nil {
			return Page{}, ErrSnapshotExpired
		}
		if current.path != path || token.Index > len(current.entries) {
			return Page{}, ErrPath
		}
		index = token.Index
	}
	if ctx.Err() != nil {
		return Page{}, ctx.Err()
	}
	end := min(len(current.entries), index+limit)
	page := Page{Entries: append([]Entry{}, current.entries[index:end]...), Snapshot: current.id}
	if end < len(current.entries) {
		raw, _ := json.Marshal(directoryCursor{Snapshot: current.id, Index: end})
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

func readErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, ErrPath):
		return "invalid_path"
	case errors.Is(err, ErrReaderCapacity):
		return "capacity"
	case errors.Is(err, ErrSnapshotExpired):
		return "resync_required"
	case errors.Is(err, ErrChangedDuringRead):
		return "changed_during_read"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	default:
		return "unavailable"
	}
}
