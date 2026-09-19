package sessionfiles

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/guest"
	"io"
	"os"
	"time"
)

type Writer struct {
	Repository workspace.GuestMutationRepository
	Backend    sandbox.Processes
	Content    workspace.PreparedStore
}

func (w Writer) WriteSession(ctx context.Context, session, owner, path string, source io.Reader, limit int64) (workspace.File, error) {
	if w.Repository == nil || w.Backend == nil || w.Content == nil {
		return workspace.File{}, fmt.Errorf("workspace writer ports required")
	}
	if _, err := workspace.CleanPath(path); err != nil {
		return workspace.File{}, err
	}
	content, err := w.Content.Prepare(ctx, source, limit)
	if err != nil {
		return workspace.File{}, err
	}
	mutation, err := w.Repository.BeginGuestWorkspaceMutation(ctx, workspace.Mutation{ID: uuid.NewString(), SessionID: session, Owner: owner, Path: path}, content)
	if err != nil {
		return workspace.File{}, err
	}
	return w.apply(ctx, mutation)
}
func (w Writer) Recover(ctx context.Context, id, owner string) (workspace.File, error) {
	if w.Repository == nil || w.Backend == nil || w.Content == nil {
		return workspace.File{}, fmt.Errorf("workspace writer ports required")
	}
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
	if !mutation.Issued {
		return w.apply(ctx, mutation)
	}
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
func (w Writer) apply(ctx context.Context, p workspace.GuestMutation) (workspace.File, error) {
	content, err := w.Content.Open(ctx, p.Content)
	if err != nil {
		return workspace.File{}, w.uncertain(ctx, p, err)
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
func (w Writer) uncertain(ctx context.Context, p workspace.GuestMutation, cause error) error {
	p.State = "uncertain"
	p.Error = cause.Error()
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(workspace.ErrUncertain, cause, w.Repository.FinishWorkspaceMutation(recordCtx, p.Mutation))
}
