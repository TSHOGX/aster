package workspace

import (
	"context"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
)

type PreparedContent struct {
	Digest string
	Size   int64
}
type PreparedStore interface {
	Prepare(context.Context, io.Reader, int64) (PreparedContent, error)
	Open(context.Context, PreparedContent) (io.ReadCloser, error)
}
type GuestMutation struct {
	Mutation
	Reference   sandbox.Reference
	Content     PreparedContent
	Issued      bool
	Maintenance bool
}
type GuestMutationRepository interface {
	BeginGuestWorkspaceMutation(context.Context, Mutation, PreparedContent, int) (GuestMutation, error)
	LoadGuestWorkspaceMutation(context.Context, string, string) (GuestMutation, error)
	MarkGuestWorkspaceMutationIssued(context.Context, GuestMutation) error
	FinishWorkspaceMutation(context.Context, Mutation) error
}
