package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspacereader"
)

const MaxWorkspaceReaders = 16
const MaxCapturedBytes int64 = 512 << 20

type readerCarrier struct {
	scope   Scope
	ref     sandbox.ProcessReference
	ctx     context.Context
	cancel  context.CancelFunc
	ready   chan struct{}
	client  *workspacereader.Client
	err     error
	uses    int
	listing bool
	used    time.Time
}

// WorkspaceReader owns read-only helper lifetimes and immutable capture leases.
// Only helper bindings are durable; a read never becomes a command or activity.
type WorkspaceReader struct {
	Store         *Store
	Lifecycle     *Lifecycle
	Backend       FileBackend
	Helper        string
	TempDirectory string
	mu            sync.Mutex
	entries       map[string]*readerCarrier
	volumePages   map[string]*volumePage
	volumeUses    map[string]int
	captures      int64
	requests      int
	closed        bool
	cleanupCursor string
	cleanupHandle string
	group         sync.WaitGroup
}

func newWorkspaceReader(f *Files) *WorkspaceReader {
	return &WorkspaceReader{Store: f.Store, Lifecycle: f.Lifecycle, Backend: f.Backend, Helper: f.Helper, TempDirectory: f.CaptureDirectory, entries: map[string]*readerCarrier{}}
}
func (f *Files) workspaceReader() *WorkspaceReader {
	f.readerOnce.Do(func() { f.reader = newWorkspaceReader(f) })
	return f.reader
}
func (f *Files) List(ctx context.Context, scope Scope, query projectfiles.DirectoryQuery) (projectfiles.Page, error) {
	return f.workspaceReader().List(ctx, scope, query)
}
func (f *Files) Capture(ctx context.Context, scope Scope, path string) (*CapturedFile, error) {
	return f.workspaceReader().Capture(ctx, scope, path)
}
func (f *Files) Close() error {
	f.PreviewSnapshots().Close()
	return f.workspaceReader().Close()
}

type WorkspaceReadStats struct {
	Readers       int   `json:"reader_entries"`
	Leases        int   `json:"active_read_leases"`
	CapturedBytes int64 `json:"captured_bytes"`
	CaptureLimit  int64 `json:"capture_limit"`
}

func (f *Files) ReadStats() WorkspaceReadStats {
	readers, leases, bytes := f.workspaceReader().Stats()
	return WorkspaceReadStats{Readers: readers, Leases: leases, CapturedBytes: bytes, CaptureLimit: MaxCapturedBytes}
}

func (w *WorkspaceReader) Stats() (readers, requests int, bytes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.entries), w.requests, w.captures
}
func samePlacement(a sandbox.ProcessReference, b sandbox.Reference) bool {
	return a.StoreID == b.StoreID && a.SandboxID == b.SandboxID && a.Epoch == b.Epoch
}
func (w *WorkspaceReader) saved(ctx context.Context, project string) (sandbox.ProcessReference, error) {
	var ref sandbox.ProcessReference
	err := w.Store.reader.QueryRowContext(ctx, `SELECT store_id,sandbox_id,epoch,handle FROM workspace_readers WHERE project_id=?`, project).Scan(&ref.StoreID, &ref.SandboxID, &ref.Epoch, &ref.Handle)
	return ref, err
}
func (w *WorkspaceReader) forget(ctx context.Context, project, handle string) error {
	return w.Store.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM workspace_readers WHERE project_id=? AND handle=?`, project, handle)
		return err
	})
}
func (w *WorkspaceReader) retire(ctx context.Context, project string, ref sandbox.ProcessReference) error {
	current, err := w.Store.resourcesFor(ctx, project)
	if err != nil {
		return err
	}
	var state string
	if err = w.Store.reader.QueryRowContext(ctx, `SELECT state FROM projects WHERE id=?`, project).Scan(&state); err != nil {
		return err
	}
	if !samePlacement(ref, current.Reference) || state == "paused" || state == "deleted" {
		return w.forget(ctx, project, ref.Handle)
	}
	attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = w.Backend.SignalProcess(attempt, ref, 9)
	if sandbox.Is(err, sandbox.NotFound) {
		return w.forget(ctx, project, ref.Handle)
	}
	if err != nil && !sandbox.Is(err, sandbox.Precondition) {
		return err
	}
	stream, err := w.Backend.ConnectProcess(attempt, ref, 0)
	if err != nil {
		if sandbox.Is(err, sandbox.NotFound) {
			return w.forget(ctx, project, ref.Handle)
		}
		return err
	}
	defer stream.Close()
	stop := context.AfterFunc(attempt, func() { _ = stream.Close() })
	defer stop()
	for {
		frame, e := stream.Next()
		if e != nil {
			return e
		}
		if frame.Kind == "end" {
			return w.forget(ctx, project, ref.Handle)
		}
	}
}
func (w *WorkspaceReader) start(entry *readerCarrier) {
	defer w.group.Done()
	attempt, cancel := context.WithTimeout(entry.ctx, 10*time.Second)
	defer cancel()
	releaseStart, err := w.Store.beginRuntimeWork(attempt, runtimeReaderStart)
	if err != nil {
		entry.err = err
		close(entry.ready)
		return
	}
	defer releaseStart()
	if previous, err := w.saved(attempt, entry.scope.ProjectID); err == nil {
		if err = w.retire(attempt, entry.scope.ProjectID, previous); err != nil {
			entry.err = err
			close(entry.ready)
			return
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		entry.err = err
		close(entry.ready)
		return
	}
	entry.err = w.Store.write(attempt, func(tx *sql.Tx) error {
		p, err := writableProject(attempt, tx, entry.scope)
		if err != nil {
			return err
		}
		if p.State != "active" {
			return ErrPending
		}
		current, err := resources(attempt, tx, entry.scope.ProjectID)
		if err != nil {
			return err
		}
		if !samePlacement(entry.ref, current.Reference) {
			return ErrConflict
		}
		_, err = tx.ExecContext(attempt, `INSERT INTO workspace_readers VALUES(?,?,?,?,?,?)`, entry.scope.ProjectID, entry.ref.StoreID, entry.ref.SandboxID, entry.ref.Epoch, entry.ref.Handle, w.Store.timestamp())
		return err
	})
	if entry.err != nil {
		close(entry.ready)
		return
	}
	helper := w.Helper
	if helper == "" {
		helper = "/usr/local/bin/workspace-files"
	}
	// Start's stream must outlive its first HTTP consumer. The timeout cancels
	// startup only; a successful handshake transfers lifetime to the carrier.
	stop := context.AfterFunc(attempt, entry.cancel)
	stream, err := w.Backend.StartProcess(entry.ctx, sandbox.StartProcessRequest{ProcessReference: entry.ref, IdempotencyKey: entry.ref.Handle, Command: helper, Arguments: []string{"--reader"}, Directory: "/workspace"})
	if err == nil {
		entry.client, err = workspacereader.New(attempt, stream, func(ctx context.Context, data []byte) error { return w.Backend.SendProcessInput(ctx, entry.ref, data) })
	}
	stop()
	entry.err = err
	if err != nil {
		close(entry.ready)
		entry.cancel()
		return
	}
	releaseStart() // Handshake is settled; idle carriers are not release blockers.
	close(entry.ready)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	defer entry.client.Close()
	for {
		select {
		case <-entry.ctx.Done():
			return
		case <-ticker.C:
		}
		w.mu.Lock()
		idle := entry.uses == 0 && time.Since(entry.used) > 25*time.Second
		w.mu.Unlock()
		w.mu.Lock()
		unused := entry.uses == 0
		w.mu.Unlock()
		if idle || unused && !entry.client.Alive() {
			entry.cancel()
			return
		}
		check, done := context.WithTimeout(entry.ctx, 2*time.Second)
		current, e := w.Lifecycle.ActiveResources(check, entry.scope)
		done()
		if e != nil || !samePlacement(entry.ref, current.Reference) {
			entry.cancel()
			return
		}
	}
}
func (w *WorkspaceReader) acquire(ctx context.Context, scope Scope, list bool) (*readerCarrier, context.Context, func(), error) {
	releaseWork, err := w.Store.beginRuntimeWork(ctx, runtimeRead)
	if err != nil {
		return nil, nil, nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			releaseWork()
		}
	}()
	current, err := w.Lifecycle.ActiveResources(ctx, scope)
	if err != nil {
		return nil, nil, nil, err
	}
	w.mu.Lock()
	if w.closed || w.requests >= 16 {
		w.mu.Unlock()
		return nil, nil, nil, ErrCapacity
	}
	entry := w.entries[scope.ProjectID]
	if entry != nil {
		select {
		case <-entry.ready:
			if entry.err != nil || entry.ctx.Err() != nil || entry.client == nil || !entry.client.Alive() {
				if entry.uses > 0 && entry.ctx.Err() == nil {
					w.mu.Unlock()
					return nil, nil, nil, ErrPending
				}
				entry.cancel()
				delete(w.entries, scope.ProjectID)
				entry = nil
			}
		default:
		}
	}
	if entry != nil && !samePlacement(entry.ref, current.Reference) {
		entry.cancel()
		delete(w.entries, scope.ProjectID)
		entry = nil
	}
	if entry == nil {
		if len(w.entries) >= MaxWorkspaceReaders {
			w.mu.Unlock()
			return nil, nil, nil, ErrCapacity
		}
		lifetime, cancel := context.WithCancel(context.Background())
		entry = &readerCarrier{scope: scope, ref: sandbox.ProcessReference{StoreID: current.Reference.StoreID, SandboxID: current.Reference.SandboxID, Epoch: current.Reference.Epoch, Handle: "reader-" + uuid.NewString()}, ctx: lifetime, cancel: cancel, ready: make(chan struct{}), used: time.Now()}
		w.entries[scope.ProjectID] = entry
		w.group.Add(1)
		go w.start(entry)
	}
	if entry.uses >= 2 || list && entry.listing {
		w.mu.Unlock()
		return nil, nil, nil, ErrCapacity
	}
	entry.uses++
	if list {
		entry.listing = true
	}
	entry.used = time.Now()
	w.requests++
	w.mu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	stop := context.AfterFunc(entry.ctx, cancel)
	var once sync.Once
	release := func() {
		once.Do(func() {
			stop()
			cancel()
			w.mu.Lock()
			entry.uses--
			entry.used = time.Now()
			if list {
				entry.listing = false
			}
			w.requests--
			w.mu.Unlock()
			releaseWork()
		})
	}
	select {
	case <-requestCtx.Done():
		release()
		return nil, nil, nil, requestCtx.Err()
	case <-entry.ready:
	}
	if entry.err != nil {
		release()
		return nil, nil, nil, entry.err
	}
	transferred = true
	return entry, requestCtx, release, nil
}
func (w *WorkspaceReader) validate(ctx context.Context, entry *readerCarrier) error {
	current, err := w.Lifecycle.ActiveResources(ctx, entry.scope)
	if err != nil {
		return err
	}
	if !samePlacement(entry.ref, current.Reference) {
		return ErrConflict
	}
	return nil
}

type workspaceCursor struct {
	Project, Path string
	Epoch         uint64
	Cursor        string
}

func (w *WorkspaceReader) List(ctx context.Context, scope Scope, query projectfiles.DirectoryQuery) (projectfiles.Page, error) {
	if query.Validate() != nil {
		return projectfiles.Page{}, ErrInvalid
	}
	path, cursor, limit := query.Path, query.Cursor, query.Limit
	r, readctx, _, done, paused, err := w.pausedVolume(ctx, scope)
	if paused {
		if err != nil {
			return projectfiles.Page{}, err
		}
		defer done()
		return w.listPaused(readctx, scope, r, query)
	}
	if cursor != "" {
		var token struct{ ID string }
		raw, _ := base64.RawURLEncoding.DecodeString(cursor)
		if json.Unmarshal(raw, &token) == nil && token.ID != "" {
			return projectfiles.Page{}, ErrResync
		}
	}
	entry, ctx, release, err := w.acquire(ctx, scope, true)
	if err != nil {
		return projectfiles.Page{}, err
	}
	defer release()
	var token workspaceCursor
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if len(cursor) > 4096 || e != nil || json.Unmarshal(raw, &token) != nil || token.Project != scope.ProjectID || token.Path != path {
			return projectfiles.Page{}, ErrInvalid
		}
		if token.Epoch != entry.ref.Epoch {
			return projectfiles.Page{}, ErrResync
		}
	}
	var page projectfiles.Page
	err = entry.client.Call(ctx, projectfiles.ReadRequest{Operation: "list", Path: path, Cursor: token.Cursor, Limit: limit, Name: query.Name, Seek: query.Seek}, func(frame projectfiles.ReadFrame) error {
		if frame.Kind != "complete" || frame.Page == nil || len(frame.Page.Entries) > limit {
			return ErrInvalid
		}
		page = *frame.Page
		return nil
	})
	if err != nil {
		return page, readerError(err)
	}
	if err = w.validate(ctx, entry); err != nil {
		return projectfiles.Page{}, err
	}
	if page.Next != "" {
		raw, _ := json.Marshal(workspaceCursor{Project: scope.ProjectID, Path: path, Epoch: entry.ref.Epoch, Cursor: page.Next})
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	if page.Previous != "" {
		raw, _ := json.Marshal(workspaceCursor{Project: scope.ProjectID, Path: path, Epoch: entry.ref.Epoch, Cursor: page.Previous})
		page.Previous = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
func readerError(err error) error {
	var remote *workspacereader.RemoteError
	if errors.As(err, &remote) {
		switch remote.Code {
		case "invalid_path":
			return ErrInvalid
		case "not_found", "unavailable":
			return ErrNotFound
		case "capacity":
			return ErrCapacity
		case "resync_required":
			return ErrResync
		case "changed_during_read":
			return projectfiles.ErrChangedDuringRead
		case "canceled":
			return context.Canceled
		}
	}
	if errors.Is(err, workspacereader.ErrCapacity) {
		return ErrCapacity
	}
	return err
}

type CapturedFile struct {
	file     *os.File
	Metadata projectfiles.ReadMetadata
	Digest   string
	ctx      context.Context
	release  func()
	once     sync.Once
}

func (c *CapturedFile) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.file.Read(p)
}
func (c *CapturedFile) Seek(offset int64, whence int) (int64, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.file.Seek(offset, whence)
}
func (c *CapturedFile) Close() error {
	var err error
	c.once.Do(func() { err = c.file.Close(); c.release() })
	return err
}
func (w *WorkspaceReader) Capture(ctx context.Context, scope Scope, path string) (out *CapturedFile, result error) {
	return w.CaptureLimited(ctx, scope, path, projectfiles.MaxTransfer)
}
func (w *WorkspaceReader) CaptureLimited(ctx context.Context, scope Scope, path string, maxBytes int64) (out *CapturedFile, result error) {
	if projectfiles.Path(path, false) != nil {
		return nil, ErrInvalid
	}
	r, readctx, unlock, done, paused, err := w.pausedVolume(ctx, scope)
	if paused {
		if err != nil {
			return nil, err
		}
		return w.capturePaused(readctx, scope, r, path, maxBytes, unlock, done)
	}
	entry, ctx, release, err := w.acquire(ctx, scope, false)
	if err != nil {
		return nil, err
	}
	var reserved int64
	free := func() { w.mu.Lock(); w.captures -= reserved; w.mu.Unlock(); release() }
	defer func() {
		if result != nil {
			free()
		}
	}()
	directory := w.TempDirectory
	if directory == "" {
		directory = os.TempDir()
	}
	fd, err := unix.Open(directory, unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "workspace-capture")
	defer file.Close()
	var metadata *projectfiles.ReadMetadata
	hash := sha256.New()
	var size int64
	digest := ""
	err = entry.client.Call(ctx, projectfiles.ReadRequest{Operation: "read", Path: path}, func(frame projectfiles.ReadFrame) error {
		switch frame.Kind {
		case "metadata":
			if metadata != nil || frame.Metadata == nil || frame.Metadata.Size < 0 || frame.Metadata.Size > projectfiles.MaxTransfer {
				return ErrInvalid
			}
			if frame.Metadata.Size > maxBytes {
				return ErrPreviewSourceTooLarge
			}
			w.mu.Lock()
			if w.captures+frame.Metadata.Size > MaxCapturedBytes {
				w.mu.Unlock()
				return ErrCapacity
			}
			reserved = frame.Metadata.Size
			w.captures += reserved
			w.mu.Unlock()
			metadata = frame.Metadata
		case "data":
			if metadata == nil || frame.Offset != size || len(frame.Data) > projectfiles.ReaderChunkBytes {
				return ErrInvalid
			}
			if size+int64(len(frame.Data)) > metadata.Size {
				return projectfiles.ErrChangedDuringRead
			}
			n, e := io.MultiWriter(file, hash).Write(frame.Data)
			size += int64(n)
			return e
		case "complete":
			if metadata == nil || frame.Size != size || size != metadata.Size {
				return projectfiles.ErrChangedDuringRead
			}
			actual := hex.EncodeToString(hash.Sum(nil))
			if frame.Digest != actual {
				return errors.New("workspace capture digest mismatch")
			}
			digest = actual
		default:
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return nil, readerError(err)
	}
	if digest == "" {
		return nil, ErrInvalid
	}
	if err = w.validate(ctx, entry); err != nil {
		return nil, err
	}
	// Reopen the unlinked inode read-only before handing it to HTTP. No pathname
	// can select another capture and no exposed handle can alter its contents.
	captured, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return nil, err
	}
	return &CapturedFile{file: captured, Metadata: *metadata, Digest: digest, ctx: ctx, release: free}, nil
}
func (w *WorkspaceReader) Reconcile(ctx context.Context) error {
	w.mu.Lock()
	cursor := w.cleanupCursor
	handle := w.cleanupHandle
	w.mu.Unlock()
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT project_id,store_id,sandbox_id,epoch,handle FROM workspace_readers WHERE (project_id,handle)>(?,?) ORDER BY project_id,handle LIMIT 32`, cursor, handle)
	if err != nil {
		return err
	}
	type saved struct {
		project string
		ref     sandbox.ProcessReference
	}
	list := []saved{}
	for rows.Next() {
		var row saved
		if err = rows.Scan(&row.project, &row.ref.StoreID, &row.ref.SandboxID, &row.ref.Epoch, &row.ref.Handle); err != nil {
			break
		}
		list = append(list, row)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	w.mu.Lock()
	if len(list) > 0 {
		w.cleanupCursor = list[len(list)-1].project
		w.cleanupHandle = list[len(list)-1].ref.Handle
	} else {
		w.cleanupCursor = ""
		w.cleanupHandle = ""
	}
	w.mu.Unlock()
	var failures []error
	for _, row := range list {
		w.mu.Lock()
		entry := w.entries[row.project]
		active := entry != nil && entry.ctx.Err() == nil
		if entry != nil {
			select {
			case <-entry.ready:
				active = active && entry.err == nil
			default:
			}
		}
		if !active && entry != nil {
			entry.cancel()
			delete(w.entries, row.project)
		}
		w.mu.Unlock()
		if !active {
			failures = append(failures, w.retire(ctx, row.project, row.ref))
		}
	}
	return errors.Join(failures...)
}
func (w *WorkspaceReader) Close() error {
	w.mu.Lock()
	w.closed = true
	w.volumePages = nil
	for _, entry := range w.entries {
		entry.cancel()
	}
	w.mu.Unlock()
	w.group.Wait()
	w.mu.Lock()
	w.cleanupCursor = ""
	w.cleanupHandle = ""
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.Reconcile(ctx)
}

// Keep a compile-time guarantee that completed captures are seekable, immutable
// representations suitable for a single HTTP byte range.
var _ io.ReadSeeker = (*CapturedFile)(nil)
