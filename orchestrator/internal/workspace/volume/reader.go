// Package volume reads a paused workspace while an application lease prevents
// writer activation. The engine confines paths; this adapter additionally
// rejects symlinks at every component to preserve product file semantics.
package volume

import (
	"context"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Backend interface {
	sandbox.Lifecycle
	sandbox.VolumeReads
}
type Files struct {
	Repository       workspace.ReadLeaseRepository
	Backend          Backend
	SessionID, Owner string
}

func (f Files) Open(ctx context.Context, name string) (io.ReadSeekCloser, workspace.File, error) {
	if f.Repository == nil || f.Backend == nil {
		return nil, workspace.File{}, fmt.Errorf("offline workspace read ports required")
	}
	if _, err := workspace.CleanPath(name); err != nil {
		return nil, workspace.File{}, err
	}
	lease, err := f.Repository.BeginOfflineWorkspaceRead(ctx, f.SessionID, f.Owner, uuid.NewString())
	if err != nil {
		return nil, workspace.File{}, err
	}
	r := &reader{ctx: ctx, files: f, lease: lease, name: name}
	success := false
	defer func() {
		if !success {
			r.Close()
		}
	}()
	observed, err := f.Backend.Inspect(ctx, lease.Reference)
	if err != nil {
		return nil, workspace.File{}, err
	}
	if observed.Reference != lease.Reference || observed.State != "paused" || observed.PlacementState != "lost" {
		return nil, workspace.File{}, workspace.ErrMutationBusy
	}
	var entry sandbox.VolumeEntry
	parts := strings.Split(name, "/")
	for i := range parts {
		entry, err = f.Backend.LstatVolume(ctx, lease.Volume, strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, workspace.File{}, readError(err)
		}
		kind := "directory"
		if i == len(parts)-1 {
			kind = "file"
		}
		if entry.Type != kind {
			return nil, workspace.File{}, workspace.ErrForbidden
		}
	}
	r.size = entry.Size
	success = true
	return r, workspace.File{Path: name, Size: entry.Size, ModifiedAt: entry.ModifiedAt}, nil
}
func readError(err error) error {
	if sandbox.Is(err, sandbox.NotFound) {
		return errors.Join(os.ErrNotExist, err)
	}
	return err
}

type reader struct {
	mu           sync.Mutex
	ctx          context.Context
	files        Files
	lease        workspace.ReadLease
	name         string
	size, offset int64
	closed       bool
	released     bool
}

func (r *reader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset >= r.size {
		return 0, io.EOF
	}
	length := min(int64(len(p)), int64(256<<10), r.size-r.offset)
	data, err := r.files.Backend.ReadVolume(r.ctx, r.lease.Volume, r.name, r.offset, length)
	if err != nil {
		return 0, readError(err)
	}
	if int64(len(data)) != length {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, data)
	r.offset += int64(n)
	return n, nil
}
func (r *reader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	base := int64(0)
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.offset
	case io.SeekEnd:
		base = r.size
	default:
		return r.offset, fmt.Errorf("invalid seek origin")
	}
	if (offset > 0 && base > math.MaxInt64-offset) || offset < -base {
		return r.offset, fmt.Errorf("invalid seek offset")
	}
	r.offset = base + offset
	return r.offset, nil
}
func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil
	}
	r.closed = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 5*time.Second)
	defer cancel()
	if err := r.files.Repository.EndOfflineWorkspaceRead(ctx, r.lease); err != nil {
		return err
	}
	r.released = true
	return nil
}
