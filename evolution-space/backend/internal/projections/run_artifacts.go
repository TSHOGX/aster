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

// Include every durable advertisement, including provider requests and evidence
// that do not appear in a workflow event. Filtering and pagination occur in SQL;
// a late artifact is never mistaken for missing because an event scan was capped.
const advertisedRunArtifacts = `WITH advertised(metadata) AS (
 SELECT j.value FROM run_events e, json_each(e.request,'$.artifacts') j WHERE e.run_id=:run
 UNION ALL SELECT json_extract(body,'$.artifact') FROM run_checkpoints WHERE run_id=:run
 UNION ALL SELECT json_extract(body,'$.request') FROM provider_calls WHERE run_id=:run
 UNION ALL SELECT json_extract(body,'$.output') FROM provider_outcomes WHERE run_id=:run
 UNION ALL SELECT j.value FROM provider_outcomes o, json_each(o.body,'$.evidence') j WHERE o.run_id=:run
), refs AS (
 SELECT json_extract(metadata,'$.id') id, min(metadata) metadata, count(DISTINCT metadata) variants
 FROM advertised WHERE metadata IS NOT NULL GROUP BY json_extract(metadata,'$.id')
) `

type RunArtifactPage struct {
	Items     []evolution.ArtifactRef `json:"items"`
	NextAfter string                  `json:"next_after"`
	HasMore   bool                    `json:"has_more"`
}

func (s *Store) requireArtifactRun(ctx context.Context, group, run string) error {
	if !manifest.ValidID(group) || !manifest.ValidID(run) {
		return fmt.Errorf("invalid run artifact scope")
	}
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM runs WHERE group_id=? AND id=?", group, run).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func decodeRunArtifact(group string, raw []byte, variants int) (evolution.ArtifactRef, error) {
	ref, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(raw))
	if err != nil || variants != 1 || ref.GroupID != group || ref.Validate() != nil {
		return evolution.ArtifactRef{}, ErrConflict
	}
	return ref, nil
}

// RunArtifact resolves an exact advertised artifact independently of list cursors.
// Callers must resolve current source authority before accessing metadata or bytes.
func (s *Store) RunArtifact(ctx context.Context, group, run, id string) (evolution.ArtifactRef, []byte, error) {
	if !manifest.ValidID(id) {
		return evolution.ArtifactRef{}, nil, fmt.Errorf("invalid artifact identity")
	}
	if err := s.requireArtifactRun(ctx, group, run); err != nil {
		return evolution.ArtifactRef{}, nil, err
	}
	var raw []byte
	var variants int
	err := s.db.QueryRowContext(ctx, advertisedRunArtifacts+"SELECT metadata,variants FROM refs WHERE id=:id", sql.Named("run", run), sql.Named("id", id)).Scan(&raw, &variants)
	if errors.Is(err, sql.ErrNoRows) {
		return evolution.ArtifactRef{}, nil, ErrNotFound
	}
	if err != nil {
		return evolution.ArtifactRef{}, nil, err
	}
	ref, err := decodeRunArtifact(group, raw, variants)
	if err != nil {
		return ref, nil, err
	}
	body, err := s.NonDatasetArtifact(ctx, ref)
	return ref, body, err
}

// RunArtifacts returns a bounded page of immutable advertised metadata. Bytes
// remain behind the separate current-grant download boundary.
func (s *Store) RunArtifacts(ctx context.Context, group, run, after string, limit int) (RunArtifactPage, error) {
	result := RunArtifactPage{Items: []evolution.ArtifactRef{}, NextAfter: after}
	if limit < 1 || limit > 100 || after != "" && !manifest.ValidID(after) {
		return result, fmt.Errorf("invalid artifact page")
	}
	if err := s.requireArtifactRun(ctx, group, run); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, advertisedRunArtifacts+`SELECT metadata,variants FROM refs WHERE id>:after AND NOT EXISTS(SELECT 1 FROM catalog_definitions d WHERE d.kind='profile' AND (json_extract(d.body,'$.dataset.digest')=json_extract(refs.metadata,'$.digest') OR (json_extract(d.body,'$.dataset.group_id')=json_extract(refs.metadata,'$.group_id') AND json_extract(d.body,'$.dataset.id')=refs.id))) ORDER BY id LIMIT :limit`, sql.Named("run", run), sql.Named("after", after), sql.Named("limit", limit+1))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(result.Items) == limit {
			result.HasMore = true
			break
		}
		var raw []byte
		var variants int
		if err = rows.Scan(&raw, &variants); err != nil {
			return result, err
		}
		ref, err := decodeRunArtifact(group, raw, variants)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, ref)
		result.NextAfter = ref.ID
	}
	return result, rows.Err()
}
