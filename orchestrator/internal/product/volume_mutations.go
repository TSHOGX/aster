package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

// The resource row is the durable volume-creation intent; storage_key survives
// a lost reply and is also resolved by deletion. No sandbox capacity is reserved.
// Callers hold the lifecycle lane until binding or explicit failure.
func (f *Files) ensureWorkspaceStorage(ctx context.Context, scope Scope) (Resources, error) {
	var r Resources
	err := f.Store.write(ctx, func(tx *sql.Tx) error {
		p, err := writableProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if p.State != "created" && p.State != "paused" {
			return ErrPending
		}
		var phase string
		if err = tx.QueryRowContext(ctx, `SELECT phase FROM application_release WHERE id=1`).Scan(&phase); err != nil {
			return err
		}
		if phase != "open" {
			return ErrDraining
		}
		r, err = resources(ctx, tx, p.ID)
		if errors.Is(err, ErrNotFound) {
			if err = f.Profile.Validate(); err != nil {
				return err
			}
			raw, _ := json.Marshal(f.Profile)
			_, err = tx.ExecContext(ctx, `INSERT INTO project_resources(project_id,store_id,profile,occupied,storage_key) VALUES(?,?,?,0,?)`, p.ID, f.Profile.StoreID, string(raw), "storage-"+uuid.NewString())
			if err != nil {
				return err
			}
			r, err = resources(ctx, tx, p.ID)
		}
		return err
	})
	if err != nil || r.WorkspaceVolume != "" {
		return r, err
	}
	if r.StorageKey == "" {
		return r, ErrPending
	}
	volume, err := f.Lifecycle.Backend.CreateVolume(ctx, r.StorageKey, r.Profile.WorkspaceBytes)
	if err != nil {
		return r, err
	}
	if volume.StoreID != r.Reference.StoreID || volume.VolumeID == "" || volume.Capacity != r.Profile.WorkspaceBytes {
		return r, ErrConflict
	}
	err = f.Store.write(ctx, func(tx *sql.Tx) error {
		// Bind even if a concurrent delete/resume was admitted. Its execution waits
		// on our lane and must account for the volume this original intent created.
		_, err := tx.ExecContext(ctx, `UPDATE project_resources SET workspace_volume=? WHERE project_id=? AND storage_key=? AND workspace_volume=''`, volume.VolumeID, scope.ProjectID, r.StorageKey)
		return err
	})
	r.WorkspaceVolume = volume.VolumeID
	return r, err
}
func volumeParent(ctx context.Context, backend sandbox.VolumeReads, ref sandbox.VolumeReference, name string) error {
	parent := path.Dir(name)
	if parent == "." {
		parent = ""
	}
	_, err := checkVolumePath(ctx, backend, ref, parent, true)
	return err
}
func volumeLeaf(ctx context.Context, backend sandbox.VolumeReads, ref sandbox.VolumeReference, name string, missingOK bool) (sandbox.VolumeEntry, error) {
	if err := volumeParent(ctx, backend, ref, name); err != nil {
		return sandbox.VolumeEntry{}, err
	}
	entry, err := backend.LstatVolume(ctx, ref, name)
	if sandbox.Is(err, sandbox.NotFound) && missingOK {
		return sandbox.VolumeEntry{}, nil
	}
	if err != nil {
		return entry, volumeFileError(err)
	}
	if entry.Type != "file" && entry.Type != "directory" {
		return entry, ErrInvalid
	}
	return entry, nil
}
func (f *Files) executeVolume(ctx context.Context, scope Scope, id string, request projectfiles.Request, input io.Reader) (FileOperation, error) {
	w := f.workspaceReader()
	_, runctx, _, release, paused, err := w.pausedVolume(ctx, scope)
	if err != nil {
		return FileOperation{}, err
	}
	if !paused {
		return FileOperation{}, ErrPending
	}
	defer release()
	r, err := f.ensureWorkspaceStorage(runctx, scope)
	if err != nil {
		return FileOperation{}, err
	}
	backend := f.Backend
	f.mu.Lock()
	if len(f.active) >= 16 {
		f.mu.Unlock()
		return FileOperation{}, ErrCapacity
	}
	o, fresh, err := f.Store.reserveFileKind(runctx, scope, id, request, true)
	if err == nil && fresh {
		if f.active == nil {
			f.active = map[string]bool{}
		}
		f.active[id] = true
	}
	f.mu.Unlock()
	if err != nil {
		return o, err
	}
	if !fresh {
		return o, fileResult(o)
	}
	defer func() { f.mu.Lock(); delete(f.active, id); f.mu.Unlock() }()
	if o.VolumeID != r.WorkspaceVolume {
		return o, ErrConflict
	}
	ref := volumeReference(r)
	issued := false
	err = func() error {
		leaf, e := volumeLeaf(runctx, backend, ref, request.Path, request.Operation == "write" || request.Operation == "mkdir")
		if e != nil {
			return e
		}
		switch request.Operation {
		case "mkdir":
			if leaf.Type != "" {
				return ErrConflict
			}
			issued = true
			return backend.MkdirVolume(runctx, ref, request.Path)
		case "remove":
			issued = true
			return backend.RemoveVolumePath(runctx, ref, request.Path)
		case "move":
			if _, e = volumeLeaf(runctx, backend, ref, request.Destination, true); e != nil {
				return e
			}
			issued = true
			return backend.MoveVolume(runctx, ref, request.Path, request.Destination)
		case "write":
			if leaf.Type != "" && leaf.Type != "file" {
				return ErrInvalid
			}
			if input == nil {
				input = strings.NewReader("")
			}
			if closer, ok := input.(io.ReadCloser); ok {
				stop := context.AfterFunc(runctx, func() { _ = closer.Close() })
				defer stop()
			}
			// Verify length/digest in unnamed staging before touching the volume. The
			// remote temporary path is unique to this durable operation, never a target.
			w.mu.Lock()
			if w.captures+request.Size > MaxCapturedBytes {
				w.mu.Unlock()
				return ErrCapacity
			}
			w.captures += request.Size
			w.mu.Unlock()
			defer func() { w.mu.Lock(); w.captures -= request.Size; w.mu.Unlock() }()
			directory := f.CaptureDirectory
			if directory == "" {
				directory = os.TempDir()
			}
			fd, e := unix.Open(directory, unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
			if e != nil {
				return e
			}
			stage := os.NewFile(uintptr(fd), "volume-upload")
			defer stage.Close()
			hash := sha256.New()
			size, e := io.Copy(io.MultiWriter(stage, hash), io.LimitReader(input, request.Size+1))
			if e != nil {
				return e
			}
			if size != request.Size || hex.EncodeToString(hash.Sum(nil)) != request.Digest {
				return ErrInvalid
			}
			if runctx.Err() != nil {
				return runctx.Err()
			}
			if _, e = stage.Seek(0, io.SeekStart); e != nil {
				return e
			}
			issued = true
			if e = backend.ImportVolume(runctx, ref, o.StagingPath, stage, request.Size); e != nil {
				return e
			}
			return backend.MoveVolume(runctx, ref, o.StagingPath, request.Path)
		}
		return ErrInvalid
	}()
	state, detail, settled := "succeeded", "", true
	if err != nil {
		state, detail = "failed", err.Error()
		if issued && !sandbox.Is(err, sandbox.Invalid) && !sandbox.Is(err, sandbox.Conflict) && !sandbox.Is(err, sandbox.NotFound) && !sandbox.Is(err, sandbox.Precondition) {
			state, settled = "unknown", false
		}
	}
	finishctx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	persist := f.Store.finishFile(finishctx, o, state, detail, settled)
	o.State, o.Error, o.Settled = state, detail, settled
	if persist != nil {
		return o, errors.Join(err, persist)
	}
	if !settled {
		return o, errors.Join(ErrFileOutcomeUnknown, err)
	}
	if err != nil {
		return o, errors.Join(ErrFileOperationFailed, err)
	}
	return o, nil
}
