package workspace

import (
	"context"
	"errors"
	"time"
)

var ErrMutationBusy = errors.New("workspace mutation in progress")

type Mutation struct {
	ID, SessionID, Owner, Path string
	State, Error               string
	File                       File
	CreatedAt                  time.Time
}
type MutationRepository interface {
	BeginWorkspaceMutation(context.Context, Mutation) error
	FinishWorkspaceMutation(context.Context, Mutation) error
}
