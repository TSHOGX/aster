package sessionfiles

import (
	"context"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/guest"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Writer struct {
	mu          sync.Mutex
	sessions    map[string]*writeLock
	Repository  WriterRepository
	Backend     sandbox.Processes
	Lifecycle   sandbox.Lifecycle
	Content     workspace.PreparedStore
	MaxActive   int
	Provisioner WorkspaceProvisioner
}

type writeLock struct {
	token chan struct{}
	users int
}

func (w *Writer) lockSession(ctx context.Context, session string) (func(), error) {
	w.mu.Lock()
	if w.sessions == nil {
		w.sessions = map[string]*writeLock{}
	}
	lock := w.sessions[session]
	if lock == nil {
		lock = &writeLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		w.sessions[session] = lock
	}
	lock.users++
	w.mu.Unlock()
	releaseUser := func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(w.sessions, session)
		}
	}
	select {
	case <-ctx.Done():
		releaseUser()
		return nil, ctx.Err()
	case <-lock.token:
	}
	return func() { lock.token <- struct{}{}; releaseUser() }, nil
}

type WorkspaceProvisioner interface {
	EnsureWorkspace(context.Context, string, string, int) error
}

type WriterRepository interface {
	workspace.GuestMutationRepository
	execution.EnvironmentRepository
	NextGuestWorkspaceMaintenance(context.Context, string, string) (execution.WorkspaceMaintenance, error)
}

func (w *Writer) WriteSession(ctx context.Context, session, owner, path string, source io.Reader, limit int64) (workspace.File, error) {
	if w.Repository == nil || w.Backend == nil || w.Content == nil {
		return workspace.File{}, fmt.Errorf("workspace writer ports required")
	}
	if _, err := workspace.CleanPath(path); err != nil {
		return workspace.File{}, err
	}
	releaseContent, err := w.Content.Hold(ctx)
	if err != nil {
		return workspace.File{}, err
	}
	defer releaseContent()
	content, err := w.Content.Prepare(ctx, source, limit)
	if err != nil {
		return workspace.File{}, err
	}
	unlock, err := w.lockSession(ctx, session)
	if err != nil {
		return workspace.File{}, err
	}
	defer unlock()
	if w.Provisioner != nil {
		if err := w.Provisioner.EnsureWorkspace(ctx, session, owner, w.MaxActive); err != nil {
			return workspace.File{}, err
		}
	}
	mutation, err := w.Repository.BeginGuestWorkspaceMutation(ctx, workspace.Mutation{ID: uuid.NewString(), SessionID: session, Owner: owner, Path: path}, content, w.MaxActive)
	if err != nil {
		return workspace.File{}, err
	}
	// The committed mutation is now the durable reference protecting recovery.
	releaseContent()
	return w.recoverLocked(ctx, mutation.ID, owner)
}
func (w *Writer) Recover(ctx context.Context, id, owner string) (workspace.File, error) {
	if w.Repository == nil || w.Backend == nil || w.Content == nil {
		return workspace.File{}, fmt.Errorf("workspace writer ports required")
	}
	session, err := w.Repository.GuestWorkspaceMutationSession(ctx, id)
	if err != nil {
		return workspace.File{}, err
	}
	unlock, err := w.lockSession(ctx, session)
	if err != nil {
		return workspace.File{}, err
	}
	defer unlock()
	return w.recoverLocked(ctx, id, owner)
}
func (w *Writer) recoverLocked(ctx context.Context, id, owner string) (workspace.File, error) {
	for {
		mutation, err := w.Repository.LoadGuestWorkspaceMutation(ctx, id, owner)
		if err != nil {
			return workspace.File{}, err
		}
		if mutation.State == "succeeded" {
			return mutation.File, nil
		}
		if mutation.State == "failed" {
			return mutation.File, fmt.Errorf("workspace mutation failed: %s", mutation.Error)
		}
		if mutation.Maintenance {
			if w.Lifecycle == nil {
				return workspace.File{}, fmt.Errorf("workspace maintenance lifecycle port required")
			}
			state, err := w.Repository.NextGuestWorkspaceMaintenance(ctx, id, owner)
			if err != nil {
				return workspace.File{}, err
			}
			if state.Stage == "complete" {
				continue
			}
			if state.Stage != "writing" {
				op := state.Operation
				if _, err := (execution.EnvironmentService{Repository: w.Repository, Backend: w.Lifecycle}).Run(ctx, execution.OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request}); err != nil {
					return workspace.File{}, err
				}
				continue
			}
			mutation, err = w.Repository.LoadGuestWorkspaceMutation(ctx, id, owner)
			if err != nil {
				return workspace.File{}, err
			}
		}
		var file workspace.File
		if !mutation.Issued {
			file, err = w.apply(ctx, mutation)
		} else {
			file, err = w.reconcile(ctx, mutation)
		}
		if err != nil && mutation.Maintenance {
			state, nextErr := w.Repository.NextGuestWorkspaceMaintenance(ctx, id, owner)
			if nextErr != nil {
				return file, errors.Join(err, nextErr)
			}
			if state.Stage == "pausing" {
				continue
			}
		}
		if err != nil || !mutation.Maintenance {
			return file, err
		}
	}
}
func (w *Writer) reconcile(ctx context.Context, mutation workspace.GuestMutation) (workspace.File, error) {
	files := guest.Files{Backend: w.Backend, Reference: mutation.Reference, Root: "/workspace"}
	if err := files.StopReplacement(ctx, mutation.ID); err != nil {
		return workspace.File{}, w.uncertain(ctx, mutation, err)
	}
	if err := files.CleanupReplacement(ctx, mutation.ID, mutation.Path); err != nil {
		return workspace.File{}, w.uncertain(ctx, mutation, err)
	}
	file, err := files.Sync(ctx, "verify-"+uuid.NewString(), mutation.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, workspace.ErrForbidden) {
		return file, w.uncertain(ctx, mutation, err)
	}
	if err != nil || file.Digest != mutation.Content.Digest || file.Size != mutation.Content.Size {
		// Writer termination is proven, but mismatch must not become an
		// automatic overwrite. The caller may submit a deliberate new upload.
		cause := fmt.Errorf("terminated workspace write did not establish prepared content: %w", errors.Join(err, workspace.ErrUncertain))
		mutation.State = "failed"
		mutation.Error = cause.Error()
		return file, errors.Join(cause, w.Repository.FinishWorkspaceMutation(ctx, mutation.Mutation))
	}
	mutation.State = "succeeded"
	mutation.File = file
	return file, w.Repository.FinishWorkspaceMutation(ctx, mutation.Mutation)
}
func (w *Writer) apply(ctx context.Context, p workspace.GuestMutation) (workspace.File, error) {
	content, err := w.Content.Open(ctx, p.Content)
	if err != nil {
		// Nothing was issued remotely, so this preparation failure has a known
		// outcome. Maintenance still needs its final verified pause.
		p.State = "failed"
		p.Error = err.Error()
		return workspace.File{}, errors.Join(err, w.Repository.FinishWorkspaceMutation(ctx, p.Mutation))
	}
	defer content.Close()
	if err := w.Repository.MarkGuestWorkspaceMutationIssued(ctx, p); err != nil {
		return workspace.File{}, err
	}
	file, err := (guest.Files{Backend: w.Backend, Reference: p.Reference, Root: "/workspace"}).ReplacePrepared(ctx, p.ID, p.Path, p.Content.Size, p.Content.Digest, content)
	if err != nil {
		return file, w.uncertain(ctx, p, err)
	}
	p.State = "succeeded"
	p.File = file
	return file, w.Repository.FinishWorkspaceMutation(ctx, p.Mutation)
}
func (w *Writer) uncertain(ctx context.Context, p workspace.GuestMutation, cause error) error {
	p.State = "uncertain"
	p.Error = cause.Error()
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(workspace.ErrUncertain, cause, w.Repository.FinishWorkspaceMutation(recordCtx, p.Mutation))
}
