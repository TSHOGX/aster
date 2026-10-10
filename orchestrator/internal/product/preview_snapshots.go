package product

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/projectfiles"
)

const PreviewSnapshotLifetime = 5 * time.Minute
const MaxPreviewSourceBytes int64 = 100 << 20
const MaxPreviewSnapshotBytes int64 = 256 << 20
const MaxPreviewLeases = 64

var ErrSnapshotExpired = errors.New("preview snapshot expired")
var ErrPreviewSourceTooLarge = errors.New("preview source too large")

type PreviewSnapshot struct {
	ID      string    `json:"id"`
	Digest  string    `json:"digest"`
	Size    int64     `json:"size"`
	Expires time.Time `json:"expires"`
}
type previewCapture struct {
	file   *os.File
	digest string
	size   int64
	refs   int
}
type previewLease struct {
	PreviewSnapshot
	scope   Scope
	capture *previewCapture
	timer   *time.Timer
}
type previewPending struct {
	ready     chan struct{}
	cancel    context.CancelFunc
	consumers int
	capture   *previewCapture
	err       error
}

// PreviewSnapshots retains unlinked read-only inodes, independently of guest
// placement and runtime activity. IDs are leases, never authorization credentials.
// A source open always captures fresh bytes; only simultaneous opens share work.
type PreviewSnapshots struct {
	files   *Files
	mu      sync.Mutex
	leases  map[string]*previewLease
	pending map[string]*previewPending
	bytes   int64
	closed  bool
	group   sync.WaitGroup
}

func (f *Files) PreviewSnapshots() *PreviewSnapshots {
	f.previewOnce.Do(func() {
		f.previews = &PreviewSnapshots{files: f, leases: map[string]*previewLease{}, pending: map[string]*previewPending{}}
	})
	return f.previews
}
func (s *PreviewSnapshots) authorize(ctx context.Context, scope Scope) error {
	p, err := projectAction(ctx, s.files.Store.reader, scope, ActionFilesRead)
	if err != nil {
		return err
	}
	if p.State == "deleting" || p.State == "deleted" {
		return ErrDeleting
	}
	return nil
}
func (s *PreviewSnapshots) Open(ctx context.Context, scope Scope, path string, maxBytes int64) (PreviewSnapshot, error) {
	if projectfiles.Path(path, false) != nil || maxBytes <= 0 || maxBytes > MaxPreviewSourceBytes {
		return PreviewSnapshot{}, ErrInvalid
	}
	if err := s.authorize(ctx, scope); err != nil {
		return PreviewSnapshot{}, err
	}
	key := scope.Actor + "\x00" + scope.ProjectID + "\x00" + path + "\x00" + fmt.Sprint(maxBytes)
	s.mu.Lock()
	if s.closed || len(s.leases) >= MaxPreviewLeases {
		s.mu.Unlock()
		return PreviewSnapshot{}, ErrCapacity
	}
	pending := s.pending[key]
	if pending == nil {
		if len(s.pending) >= 2 {
			s.mu.Unlock()
			return PreviewSnapshot{}, ErrCapacity
		}
		captureCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		pending = &previewPending{ready: make(chan struct{}), cancel: cancel}
		s.pending[key] = pending
		s.group.Add(1)
		go s.capture(captureCtx, scope, path, key, maxBytes, pending)
	}
	pending.consumers++
	s.mu.Unlock()
	defer s.releasePending(key, pending)
	select {
	case <-ctx.Done():
		return PreviewSnapshot{}, ctx.Err()
	case <-pending.ready:
	}
	if err := ctx.Err(); err != nil {
		return PreviewSnapshot{}, err
	}
	if err := s.authorize(ctx, scope); err != nil {
		return PreviewSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending.err != nil {
		return PreviewSnapshot{}, pending.err
	}
	if s.closed || len(s.leases) >= MaxPreviewLeases {
		return PreviewSnapshot{}, ErrCapacity
	}
	if pending.capture.size > maxBytes {
		return PreviewSnapshot{}, ErrPreviewSourceTooLarge
	}
	descriptor := PreviewSnapshot{ID: uuid.NewString(), Digest: pending.capture.digest, Size: pending.capture.size, Expires: time.Now().Add(PreviewSnapshotLifetime)}
	lease := &previewLease{PreviewSnapshot: descriptor, scope: scope, capture: pending.capture}
	pending.capture.refs++
	s.leases[descriptor.ID] = lease
	lease.timer = time.AfterFunc(PreviewSnapshotLifetime, func() { s.expire(descriptor.ID) })
	return descriptor, nil
}
func (s *PreviewSnapshots) capture(ctx context.Context, scope Scope, path, key string, maxBytes int64, pending *previewPending) {
	defer s.group.Done()
	capture, err := s.files.workspaceReader().CaptureLimited(ctx, scope, path, maxBytes)
	var retained *previewCapture
	if err == nil {
		if capture.Metadata.Size > MaxPreviewSourceBytes {
			err = ErrPreviewSourceTooLarge
		} else {
			// Reopening the same inode neither copies nor rehashes source bytes. Closing
			// the transport capture releases its activity and request lease immediately.
			var file *os.File
			file, err = os.Open(fmt.Sprintf("/proc/self/fd/%d", capture.file.Fd()))
			if err == nil {
				retained = &previewCapture{file: file, digest: capture.Digest, size: capture.Metadata.Size, refs: 1}
			}
		}
		capture.Close()
	}
	pending.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && (s.closed || s.bytes+retained.size > MaxPreviewSnapshotBytes) {
		retained.file.Close()
		retained = nil
		err = ErrCapacity
	}
	if retained != nil {
		s.bytes += retained.size
	}
	pending.capture, pending.err = retained, err
	close(pending.ready)
	if pending.consumers == 0 {
		delete(s.pending, key)
		if retained != nil {
			s.drop(retained)
		}
	}
}
func (s *PreviewSnapshots) drop(c *previewCapture) {
	c.refs--
	if c.refs == 0 {
		c.file.Close()
		s.bytes -= c.size
	}
}
func (s *PreviewSnapshots) releasePending(key string, p *previewPending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.consumers--
	if p.consumers == 0 {
		p.cancel()
		select {
		case <-p.ready:
			delete(s.pending, key)
			if p.capture != nil {
				s.drop(p.capture)
			}
		default:
		}
	}
}
func (s *PreviewSnapshots) expire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease := s.leases[id]; lease != nil {
		delete(s.leases, id)
		lease.timer.Stop()
		s.drop(lease.capture)
	}
}

// Acquire returns a request-local cursor over one immutable inode. Concurrent
// ranges never share offsets. A request must release its reference, even on abort.
func (s *PreviewSnapshots) Acquire(ctx context.Context, scope Scope, id string) (PreviewSnapshot, *io.SectionReader, func(), error) {
	s.mu.Lock()
	foreign := s.leases[id] != nil && s.leases[id].scope != scope
	s.mu.Unlock()
	if foreign {
		return PreviewSnapshot{}, nil, nil, ErrNotFound
	}

	if err := s.authorize(ctx, scope); err != nil {
		return PreviewSnapshot{}, nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease := s.leases[id]
	if lease == nil {
		return PreviewSnapshot{}, nil, nil, ErrSnapshotExpired
	}
	if lease.scope != scope {
		return PreviewSnapshot{}, nil, nil, ErrNotFound
	}
	if time.Now().After(lease.Expires) {
		return PreviewSnapshot{}, nil, nil, ErrSnapshotExpired
	}
	c := lease.capture
	if err := admitTransfer(ctx, s.files.Store.reader, scope.Actor, c.size); err != nil {
		return PreviewSnapshot{}, nil, nil, err
	}
	c.refs++
	var once sync.Once
	release := func() { once.Do(func() { s.mu.Lock(); s.drop(c); s.mu.Unlock() }) }
	return lease.PreviewSnapshot, io.NewSectionReader(c.file, 0, c.size), release, nil
}
func (s *PreviewSnapshots) Release(ctx context.Context, scope Scope, id string) error {
	if _, err := activeOwnedProject(ctx, s.files.Store.reader, scope); err != nil {
		return err
	}
	s.mu.Lock()
	lease := s.leases[id]
	if lease != nil && lease.scope != scope {
		s.mu.Unlock()
		return ErrNotFound
	}
	s.mu.Unlock()
	s.expire(id)
	return nil
}
func (s *PreviewSnapshots) Close() {
	s.mu.Lock()
	s.closed = true
	for _, p := range s.pending {
		p.cancel()
	}
	for id, lease := range s.leases {
		delete(s.leases, id)
		lease.timer.Stop()
		s.drop(lease.capture)
	}
	s.mu.Unlock()
	s.group.Wait()
}

type PreviewSourceStats struct {
	Leases    int   `json:"leases"`
	Capturing int   `json:"capturing"`
	Bytes     int64 `json:"bytes"`
	Limit     int64 `json:"limit"`
}

func (s *PreviewSnapshots) Stats() PreviewSourceStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return PreviewSourceStats{Leases: len(s.leases), Capturing: len(s.pending), Bytes: s.bytes, Limit: MaxPreviewSnapshotBytes}
}
