package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type CatalogListInput struct {
	Kind          string `json:"kind"`
	AfterID       string `json:"after_id"`
	AfterRevision int64  `json:"after_revision"`
	Limit         int    `json:"limit"`
	// SharedOnly hides run-scoped workflows (experiments and draft tests),
	// which belong to the group that ran them, from group-facing listings.
	SharedOnly bool
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
		query = "SELECT id,revision,digest FROM workflow_revisions WHERE (id>? OR (id=? AND revision>?))"
		if input.SharedOnly {
			query += " AND id NOT LIKE 'experiment-%' AND id NOT LIKE 'draft-test-%' AND id NOT LIKE 'scope-%'"
		}
		query += " ORDER BY id,revision LIMIT ?"
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

func (s *Store) CatalogDefinition(ctx context.Context, kind string, ref evolution.Revision) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if kind == "workflow" {
		return s.workflowDefinition(ctx, tx, ref)
	}
	return s.definitionBytes(ctx, tx, kind, ref)
}
func (s *Store) workflowDefinition(ctx context.Context, tx *sql.Tx, ref evolution.Revision) ([]byte, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM workflow_revisions WHERE id=? AND revision=?", ref.ID, ref.Revision).Scan(&raw); err != nil {
		return nil, err
	}
	if evolution.Digest(raw) != ref.Digest {
		return nil, ErrConflict
	}
	return raw, nil
}
func (s *Store) definitionBytes(ctx context.Context, tx *sql.Tx, kind string, ref evolution.Revision) ([]byte, error) {
	if kind != "service" && kind != "template" && kind != "profile" {
		return nil, fmt.Errorf("unsupported catalog kind")
	}
	var raw []byte
	var digest string
	if err := tx.QueryRowContext(ctx, "SELECT body,digest FROM catalog_definitions WHERE kind=? AND id=? AND revision=?", kind, ref.ID, ref.Revision).Scan(&raw, &digest); err != nil {
		return nil, err
	}
	if digest != ref.Digest {
		return nil, ErrConflict
	}
	return raw, nil
}

// LatestDefinition returns the highest published revision reference of one
// catalog ID, or ErrNotFound. It discloses reference metadata only.
func (s *Store) LatestDefinition(ctx context.Context, kind, id string) (evolution.Revision, error) {
	var ref evolution.Revision
	if (kind != "service" && kind != "template" && kind != "profile") || !manifest.ValidID(id) {
		return ref, fmt.Errorf("invalid catalog lookup")
	}
	err := s.db.QueryRowContext(ctx, "SELECT id,revision,digest FROM catalog_definitions WHERE kind=? AND id=? ORDER BY revision DESC LIMIT 1", kind, id).Scan(&ref.ID, &ref.Revision, &ref.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return ref, ErrNotFound
	}
	return ref, err
}

// RunScopedWorkflow reports whether a workflow ID was published for one run's
// experiment or draft test rather than shared through the catalog.
func RunScopedWorkflow(id string) bool {
	return strings.HasPrefix(id, "experiment-") || strings.HasPrefix(id, "draft-test-") || strings.HasPrefix(id, "scope-")
}
