package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

// First upload saves and reserves every slot, then materializes only workspace.
// Callers hold the existing Project lifecycle lane through readiness.
func (f *Files) ensureWorkspaceStorage(ctx context.Context, scope Scope, action ProductAction, bytes int64) (Resources, error) {
	var r Resources
	err := f.Store.write(ctx, func(tx *sql.Tx) error {
		p, err := mutableOwnedProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if err := authorizeAction(ctx, tx, scope.Actor, action); err != nil {
			return err
		}
		if action == ActionFilesUpload || action == ActionFilesEdit {
			if err := admitTransfer(ctx, tx, scope.Actor, bytes); err != nil {
				return err
			}
		}
		if p.State != "created" && p.State != "paused" {
			return ErrPending
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return ErrDraining
		}
		r, err = resources(ctx, tx, p.ID)
		if errors.Is(err, ErrNotFound) {
			profile, e := resolveAllocationProfile(ctx, tx, scope.Actor, p.ID, f.Bindings)
			if e != nil {
				return e
			}
			if err = f.Store.bindVolumePlan(ctx, tx, p.ID, profile, false); err != nil {
				return err
			}
			r, err = resources(ctx, tx, p.ID)
		}
		return err
	})
	if err != nil {
		return r, err
	}
	plan, ok := r.Profile.VolumeForRole("workspace")
	if !ok {
		return r, ErrInvalid
	}
	if _, err = f.Lifecycle.materializeSlot(ctx, r, plan.Key); err != nil {
		return r, err
	}
	return resources(ctx, f.Store.reader, scope.ProjectID)
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
func (f *Files) executeVolume(ctx context.Context, scope Scope, id string, action ProductAction, request projectfiles.Request, input io.Reader) (FileOperation, error) {
	w := f.workspaceReader()
	_, runctx, _, release, paused, err := w.pausedVolume(ctx, scope)
	if err != nil {
		return FileOperation{}, err
	}
	if !paused {
		return FileOperation{}, ErrPending
	}
	defer release()
	r, err := f.ensureWorkspaceStorage(runctx, scope, action, request.Size)
	if err != nil {
		return FileOperation{}, err
	}
	backend := f.Backend
	f.mu.Lock()
	if len(f.active) >= 16 {
		f.mu.Unlock()
		return FileOperation{}, ErrCapacity
	}
	o, fresh, err := f.Store.reserveFileKind(runctx, scope, id, action, request, true)
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
	if o.VolumeID != r.VolumeForRole("workspace") {
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
	finishctx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	state, detail, settled := "succeeded", "", true
	if err != nil {
		state, detail = "failed", err.Error()
		definite := sandbox.Is(err, sandbox.Invalid) || sandbox.Is(err, sandbox.Conflict) || sandbox.Is(err, sandbox.NotFound) || sandbox.Is(err, sandbox.Precondition) || sandbox.Is(err, sandbox.Capacity)
		if issued && !definite {
			state, settled = "unknown", false
		}
		if issued && definite && o.StagingPath != "" {
			// An explicit engine refusal completes that call. Reclaim its
			// partial staging file so ordinary quota failures do not consume
			// more space or permanently fence subsequent workspace operations.
			if cleanupErr := backend.RemoveVolumePath(finishctx, ref, o.StagingPath); cleanupErr != nil && !sandbox.Is(cleanupErr, sandbox.NotFound) {
				err = errors.Join(err, cleanupErr)
				detail = err.Error()
				state, settled = "unknown", false
			}
		}
	}
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
