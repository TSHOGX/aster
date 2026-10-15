package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	manifest "aster/contracts/volumemanifest"
)

// RunArtifacts returns immutable artifact metadata advertised by the exact run.
// It deliberately does not read artifact bytes; callers must use the separate
// current-grant download boundary when bytes are permitted.
func (s *Store) RunArtifacts(ctx context.Context, group, run string) ([]evolution.ArtifactRef, error) {
	if !manifest.ValidID(group) || !manifest.ValidID(run) {
		return nil, fmt.Errorf("invalid run artifact scope")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM runs WHERE group_id=? AND id=?", group, run).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT request FROM run_events WHERE run_id=? ORDER BY sequence LIMIT 1001", run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]evolution.ArtifactRef{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		event, decodeErr := evolution.Decode[evolution.Event](bytes.NewReader(raw))
		if decodeErr != nil || event.RunID != run {
			return nil, fmt.Errorf("invalid stored run event")
		}
		for _, artifact := range event.Artifacts {
			if artifact.GroupID != group || artifact.Validate() != nil {
				return nil, fmt.Errorf("invalid stored run artifact")
			}
			byID[artifact.ID] = artifact
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	checkpoint, err := s.LatestRunCheckpoint(ctx, group, run)
	if err == nil {
		if checkpoint.Artifact.GroupID != group || checkpoint.Artifact.Validate() != nil {
			return nil, fmt.Errorf("invalid stored checkpoint artifact")
		}
		byID[checkpoint.Artifact.ID] = checkpoint.Artifact
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	result := make([]evolution.ArtifactRef, 0, len(byID))
	for _, artifact := range byID {
		result = append(result, artifact)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
