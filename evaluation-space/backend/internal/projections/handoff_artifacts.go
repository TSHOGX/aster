package projections

import (
	"context"
	"fmt"

	"aster/contracts/evolution"
)

// RunResultArtifact reads only artifacts declared by the exact frozen handoff.
// A grant to one result cannot be used to enumerate or read arbitrary run traces.
func (s *Store) RunResultArtifact(ctx context.Context, scope evolution.RunResultScope, ref evolution.ArtifactRef) ([]byte, error) {
	if scope.Validate() != nil || ref.Validate() != nil {
		return nil, fmt.Errorf("invalid result artifact scope")
	}
	run, err := s.Run(ctx, scope.GroupID, scope.RunID)
	if err != nil {
		return nil, err
	}
	if run.State != "completed" || run.Fence != scope.Fence || run.BindingID != scope.BindingID || run.Pins.Base != scope.Base || run.Pins.Output != scope.Output {
		return nil, ErrConflict
	}
	handoff, err := s.Handoff(ctx, scope.GroupID, scope.RunID)
	if err != nil {
		return nil, err
	}
	if scope.Matches(handoff.Patch) != nil {
		return nil, ErrConflict
	}
	for _, entry := range handoff.Patch.Entries {
		if entry.Artifact != nil && *entry.Artifact == ref {
			return s.Artifact(ctx, ref)
		}
	}
	return nil, ErrNotFound
}
