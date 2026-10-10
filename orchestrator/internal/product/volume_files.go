package product

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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

type volumePage struct {
	project, path, volume string
	epoch                 uint64
	entries               []projectfiles.Entry
	bytes                 int
	expires               time.Time
}

// A paused volume lease shares the existing lifecycle execution lane. A resume
// may be admitted, but cannot start a guest between path validation and I/O.
// No reader starts compute or adds a durable activity row. The lane is released
// once a capture is complete, independently of its HTTP consumer's lifetime.
func (w *WorkspaceReader) pausedVolume(ctx context.Context, scope Scope) (Resources, context.Context, func(), func(), bool, error) {
	p, err := ownedProject(ctx, w.Store.reader, scope)
	if err != nil {
		return Resources{}, nil, nil, nil, true, err
	}
	if p.State != "paused" && p.State != "created" {
		return Resources{}, nil, nil, nil, false, nil
	}
	releaseWork, err := w.Store.beginRuntimeWork(ctx, runtimeRead)
	if err != nil {
		return Resources{}, nil, nil, nil, true, err
	}
	lane := w.Lifecycle.lane(scope.ProjectID)
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for !lane.TryLock() {
		select {
		case <-ctx.Done():
			releaseWork()
			return Resources{}, nil, nil, nil, true, ctx.Err()
		case <-deadline.C:
			releaseWork()
			return Resources{}, nil, nil, nil, true, ErrCapacity
		case <-ticker.C:
		}
	}
	var unlocked sync.Once
	unlock := func() { unlocked.Do(lane.Unlock) }
	fail := func(err error) (Resources, context.Context, func(), func(), bool, error) {
		unlock()
		releaseWork()
		return Resources{}, nil, nil, nil, true, err
	}
	p, err = ownedProject(ctx, w.Store.reader, scope)
	if err != nil {
		return fail(err)
	}
	if p.State != "paused" && p.State != "created" {
		return fail(ErrPending)
	}
	r, err := w.Store.Resources(ctx, scope)
	if errors.Is(err, ErrNotFound) {
		r = Resources{ProjectID: scope.ProjectID}
		err = nil
	}
	if err != nil {
		return fail(err)
	}
	if r.Reference.SandboxID != "" {
		observed, e := w.Lifecycle.Backend.Inspect(ctx, r.Reference)
		if e != nil {
			return fail(e)
		}
		if observed.State != "paused" || observed.PlacementState != "lost" {
			return fail(ErrPending)
		}
	}
	w.mu.Lock()
	if w.closed || w.requests >= 16 || w.volumeUses[scope.ProjectID] >= 2 {
		w.mu.Unlock()
		return fail(ErrCapacity)
	}
	w.requests++
	if w.volumeUses == nil {
		w.volumeUses = map[string]int{}
	}
	w.volumeUses[scope.ProjectID]++
	w.mu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	var once sync.Once
	release := func() {
		once.Do(func() {
			unlock()
			cancel()
			w.mu.Lock()
			w.requests--
			w.volumeUses[scope.ProjectID]--
			if w.volumeUses[scope.ProjectID] == 0 {
				delete(w.volumeUses, scope.ProjectID)
			}
			w.mu.Unlock()
			releaseWork()
		})
	}
	return r, requestCtx, unlock, release, true, nil
}

func volumeReference(r Resources) sandbox.VolumeReference {
	return sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: r.VolumeForRole("workspace")}
}

// Direct APIs are confined by Khora. While the lifecycle lane holds the guest
// stopped, checking every component also preserves our stricter no-symlink rule.
func checkVolumePath(ctx context.Context, backend sandbox.VolumeReads, ref sandbox.VolumeReference, name string, directory bool) (sandbox.VolumeEntry, error) {
	if projectfiles.Path(name, directory) != nil {
		return sandbox.VolumeEntry{}, ErrInvalid
	}
	var entry sandbox.VolumeEntry
	if name == "" {
		return sandbox.VolumeEntry{Type: "directory"}, nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		var err error
		entry, err = backend.LstatVolume(ctx, ref, strings.Join(parts[:i+1], "/"))
		if err != nil {
			return entry, volumeFileError(err)
		}
		expected := "directory"
		if i == len(parts)-1 && !directory {
			expected = "file"
		}
		if entry.Type != expected {
			return entry, ErrInvalid
		}
	}
	return entry, nil
}
func volumeFileError(err error) error {
	switch {
	case sandbox.Is(err, sandbox.NotFound):
		return ErrNotFound
	case sandbox.Is(err, sandbox.Invalid):
		return ErrInvalid
	case sandbox.Is(err, sandbox.Capacity):
		return ErrCapacity
	case sandbox.Is(err, sandbox.Conflict), sandbox.Is(err, sandbox.Precondition):
		return ErrConflict
	}
	return err
}
func (w *WorkspaceReader) validateVolume(ctx context.Context, scope Scope, r Resources) error {
	p, err := ownedProject(ctx, w.Store.reader, scope)
	if err != nil {
		return err
	}
	if p.State == "deleting" || p.State == "deleted" {
		return ErrDeleting
	}
	current, err := w.Store.Resources(ctx, scope)
	if err != nil {
		return err
	}
	if current.Reference.StoreID != r.Reference.StoreID || current.VolumeForRole("workspace") != r.VolumeForRole("workspace") {
		return ErrConflict
	}
	return nil
}
func (w *WorkspaceReader) listPaused(ctx context.Context, scope Scope, r Resources, query projectfiles.DirectoryQuery) (projectfiles.Page, error) {
	if _, err := projectAction(ctx, w.Store.reader, scope, ActionFilesList); err != nil {
		return projectfiles.Page{}, err
	}
	path, cursor, limit := query.Path, query.Cursor, query.Limit
	if r.VolumeForRole("workspace") == "" {
		if _, bound := r.Profile.VolumeForRole("workspace"); bound {
			return projectfiles.Page{}, ErrPending
		}
		if path == "" && cursor == "" {
			return projectfiles.Page{Entries: []projectfiles.Entry{}}, nil
		}
		return projectfiles.Page{}, ErrNotFound
	}
	backend := w.Backend
	var err error
	type token struct {
		ID    string
		Index int
	}
	var position token
	var snapshot *volumePage
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(raw, &position) != nil || position.Index < 0 {
			return projectfiles.Page{}, ErrInvalid
		}
		w.mu.Lock()
		snapshot = w.volumePages[position.ID]
		w.mu.Unlock()
		if snapshot == nil || time.Now().After(snapshot.expires) || snapshot.epoch != r.Reference.Epoch {
			return projectfiles.Page{}, ErrResync
		}
		if snapshot.project != scope.ProjectID || snapshot.path != path || snapshot.volume != r.VolumeForRole("workspace") {
			return projectfiles.Page{}, ErrInvalid
		}
	} else {
		if _, err = checkVolumePath(ctx, backend, volumeReference(r), path, true); err != nil {
			return projectfiles.Page{}, err
		}
		entries, e := backend.ListVolume(ctx, volumeReference(r), path)
		if e != nil {
			return projectfiles.Page{}, volumeFileError(e)
		}
		snapshot = &volumePage{project: scope.ProjectID, path: path, volume: r.VolumeForRole("workspace"), epoch: r.Reference.Epoch, expires: time.Now().Add(projectfiles.SnapshotLifetime)}
		for _, entry := range entries {
			if entry.Name == "" || strings.ContainsAny(entry.Name, "/\\\x00") || entry.Name == "." || entry.Name == ".." {
				return projectfiles.Page{}, ErrInvalid
			}
			snapshot.bytes += len(entry.Name)*2 + 160
			if snapshot.bytes > projectfiles.MaxSnapshotBytes || len(snapshot.entries) >= projectfiles.MaxInventoryNames {
				return projectfiles.Page{}, ErrCapacity
			}
			snapshot.entries = append(snapshot.entries, projectfiles.Entry{Name: entry.Name, Kind: entry.Type, Size: entry.Size, Modified: entry.ModifiedAt.Unix(), ModifiedNano: entry.ModifiedAt.UnixNano()})
		}
		sort.Slice(snapshot.entries, func(i, j int) bool { return snapshot.entries[i].Name < snapshot.entries[j].Name })
		position.ID = uuid.NewString()
		w.mu.Lock()
		if w.volumePages == nil {
			w.volumePages = map[string]*volumePage{}
		}
		bytes := 0
		for id, page := range w.volumePages {
			if time.Now().After(page.expires) {
				delete(w.volumePages, id)
			} else {
				bytes += page.bytes
			}
		}
		for len(w.volumePages) >= projectfiles.MaxReaderSnapshots || bytes+snapshot.bytes > projectfiles.MaxReaderSnapshotBytes {
			oldest := ""
			for id, page := range w.volumePages {
				if oldest == "" || page.expires.Before(w.volumePages[oldest].expires) {
					oldest = id
				}
			}
			if oldest == "" {
				break
			}
			bytes -= w.volumePages[oldest].bytes
			delete(w.volumePages, oldest)
		}
		w.volumePages[position.ID] = snapshot
		w.mu.Unlock()
	}
	if query.Seek != "" {
		position.Index = sort.Search(len(snapshot.entries), func(i int) bool {
			if query.Seek == "after" {
				return snapshot.entries[i].Name > query.Name
			}
			return snapshot.entries[i].Name >= query.Name
		})
		if query.Seek == "around" {
			position.Index = max(0, position.Index-limit/2)
		}
	}
	if position.Index > len(snapshot.entries) {
		return projectfiles.Page{}, ErrInvalid
	}
	end := min(position.Index+limit, len(snapshot.entries))
	page := projectfiles.Page{Entries: append([]projectfiles.Entry{}, snapshot.entries[position.Index:end]...), Snapshot: position.ID, Start: position.Index, Total: len(snapshot.entries)}
	if position.Index > 0 {
		raw, _ := json.Marshal(token{ID: position.ID, Index: max(0, position.Index-limit)})
		page.Previous = base64.RawURLEncoding.EncodeToString(raw)
	}
	if end < len(snapshot.entries) {
		position.Index = end
		raw, _ := json.Marshal(position)
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	if err = w.validateVolume(ctx, scope, r); err != nil {
		return projectfiles.Page{}, err
	}
	if _, err := projectAction(ctx, w.Store.reader, scope, ActionFilesList); err != nil {
		return projectfiles.Page{}, err
	}
	return page, nil
}
func (w *WorkspaceReader) capturePaused(ctx context.Context, scope Scope, r Resources, path string, maxBytes int64, unlock, release func()) (out *CapturedFile, result error) {
	var reserved int64
	free := func() { w.mu.Lock(); w.captures -= reserved; w.mu.Unlock(); release() }
	defer func() {
		if result != nil {
			free()
		}
	}()
	if _, err := projectAction(ctx, w.Store.reader, scope, ActionFilesRead); err != nil {
		return nil, err
	}
	if r.VolumeForRole("workspace") == "" {
		if _, bound := r.Profile.VolumeForRole("workspace"); bound {
			return nil, ErrPending
		}
		return nil, ErrNotFound
	}
	backend := w.Backend
	ref := volumeReference(r)
	before, err := checkVolumePath(ctx, backend, ref, path, false)
	if err != nil {
		return nil, err
	}
	if before.Size < 0 || before.Size > projectfiles.MaxTransfer {
		return nil, ErrCapacity
	}
	if before.Size > maxBytes {
		return nil, ErrPreviewSourceTooLarge
	}
	w.mu.Lock()
	if w.captures+before.Size > MaxCapturedBytes {
		w.mu.Unlock()
		return nil, ErrCapacity
	}
	reserved = before.Size
	w.captures += reserved
	w.mu.Unlock()
	directory := w.TempDirectory
	if directory == "" {
		directory = os.TempDir()
	}
	fd, err := unix.Open(directory, unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "volume-capture")
	defer file.Close()
	hash := sha256.New()
	for offset := int64(0); offset < before.Size; {
		data, e := backend.ReadVolume(ctx, ref, path, offset, min(256<<10, before.Size-offset))
		if e != nil {
			return nil, volumeFileError(e)
		}
		if len(data) == 0 || int64(len(data)) > before.Size-offset {
			return nil, projectfiles.ErrChangedDuringRead
		}
		if _, e = io.MultiWriter(file, hash).Write(data); e != nil {
			return nil, e
		}
		offset += int64(len(data))
	}
	after, err := checkVolumePath(ctx, backend, ref, path, false)
	if err != nil {
		return nil, err
	}
	if after.Size != before.Size || !after.ModifiedAt.Equal(before.ModifiedAt) {
		return nil, projectfiles.ErrChangedDuringRead
	}
	if err = w.validateVolume(ctx, scope, r); err != nil {
		return nil, err
	}
	if _, err := projectAction(ctx, w.Store.reader, scope, ActionFilesRead); err != nil {
		return nil, err
	}
	captured, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return nil, err
	}
	unlock()
	return &CapturedFile{file: captured, Metadata: projectfiles.ReadMetadata{Size: before.Size, ModifiedNano: before.ModifiedAt.UnixNano(), Kind: "file"}, Digest: hex.EncodeToString(hash.Sum(nil)), ctx: ctx, release: free}, nil
}
