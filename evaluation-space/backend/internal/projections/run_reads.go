package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Run reads one group's retained run. Callers must authorize current group access
// before invoking this internal storage API; immutable admission is not a grant.
func (s *Store) Run(ctx context.Context, group, id string) (evolution.Run, error) {
	var result evolution.Run
	if !manifest.ValidID(group) || !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid run scope")
	}
	var body []byte
	var state string
	var fence int64
	err := s.db.QueryRowContext(ctx, "SELECT body,state,fence FROM runs WHERE group_id=? AND id=?", group, id).Scan(&body, &state, &fence)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result, err = evolution.Decode[evolution.Run](bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	if result.ID != id || result.GroupID != group {
		return evolution.Run{}, fmt.Errorf("stored run identity mismatch")
	}
	result.State = state
	result.Fence = fence
	handoff, err := s.Handoff(ctx, group, id)
	if err == nil {
		result.Handoff = handoff.State
		result.ResultBranch = handoff.BranchID
	} else if !errors.Is(err, ErrNotFound) {
		return evolution.Run{}, err
	} else {
		result.ResultBranch = ""
		result.Handoff = "not_ready"
		if result.State == "completed" {
			result.Handoff = "pending"
		}
	}
	return result, nil
}
