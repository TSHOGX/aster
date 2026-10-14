package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"context"
	"fmt"
)

type CatalogListInput struct {
	Kind          string `json:"kind"`
	AfterID       string `json:"after_id"`
	AfterRevision int64  `json:"after_revision"`
	Limit         int    `json:"limit"`
}
type CatalogPage struct {
	Revisions    []evolution.Revision `json:"revisions"`
	NextID       string               `json:"next_id"`
	NextRevision int64                `json:"next_revision"`
	HasMore      bool                 `json:"has_more"`
}

// CatalogRevisions returns immutable reference metadata only. The caller must
// authorize catalog discovery; payload and credentials are never included here.
func (s *Store) CatalogRevisions(ctx context.Context, input CatalogListInput) (CatalogPage, error) {
	result := CatalogPage{Revisions: []evolution.Revision{}, NextID: input.AfterID, NextRevision: input.AfterRevision}
	if input.Limit < 1 || input.Limit > 100 || input.AfterRevision < 0 || (input.AfterID == "" && input.AfterRevision != 0) || (input.AfterID != "" && (!manifest.ValidID(input.AfterID) || input.AfterRevision < 1)) {
		return result, fmt.Errorf("invalid catalog page")
	}
	query := "SELECT id,revision,digest FROM catalog_definitions WHERE kind=? AND (id>? OR (id=? AND revision>?)) ORDER BY id,revision LIMIT ?"
	args := []any{input.Kind, input.AfterID, input.AfterID, input.AfterRevision, input.Limit + 1}
	switch input.Kind {
	case "workflow":
		query = "SELECT id,revision,digest FROM workflow_revisions WHERE (id>? OR (id=? AND revision>?)) ORDER BY id,revision LIMIT ?"
		args = args[1:]
	case "service", "template", "profile":
	default:
		return result, fmt.Errorf("unsupported catalog kind")
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref evolution.Revision
		if err = rows.Scan(&ref.ID, &ref.Revision, &ref.Digest); err != nil {
			return result, err
		}
		if len(result.Revisions) == input.Limit {
			result.HasMore = true
			break
		}
		if err = ref.Validate(); err != nil {
			return result, err
		}
		result.Revisions = append(result.Revisions, ref)
		result.NextID = ref.ID
		result.NextRevision = ref.Revision
	}
	return result, rows.Err()
}
