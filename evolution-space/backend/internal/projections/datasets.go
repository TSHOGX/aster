package projections

import (
	"bytes"
	"context"
	"fmt"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

// DatasetSchema marks evaluation datasets uploaded through the browser. The
// bytes are opaque to Aster; the configured evaluator defines their format.
var DatasetSchema = evolution.Digest([]byte("aster-evaluation-dataset-v1"))

// MaxBrowserDatasetBytes keeps uploads within the TLS front door body limit.
const MaxBrowserDatasetBytes int64 = 4 << 20

type DatasetPage struct {
	Items     []evolution.ArtifactRef `json:"items"`
	NextAfter string                  `json:"next_after"`
	HasMore   bool                    `json:"has_more"`
}

// GroupDatasets lists sealed evaluation datasets of one group. It returns
// references only; held-out bytes never leave through this projection.
func (s *Store) GroupDatasets(ctx context.Context, group, after string, limit int) (DatasetPage, error) {
	page := DatasetPage{Items: []evolution.ArtifactRef{}}
	if !manifest.ValidID(group) || after != "" && !manifest.ValidID(after) || limit < 1 || limit > 100 {
		return page, fmt.Errorf("invalid dataset page")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT metadata FROM artifacts WHERE group_id=? AND id>? AND expired_at=0 AND json_extract(metadata,'$.schema_digest')=? ORDER BY id LIMIT ?", group, after, DatasetSchema, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		ref, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(raw))
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, ref)
		page.NextAfter = ref.ID
	}
	return page, rows.Err()
}

// SealDataset stores one uploaded dataset. A retried upload with the same ID
// and bytes replays; a different body under the same ID conflicts.
func (s *Store) SealDataset(ctx context.Context, group, id, mediaType string, body []byte) (evolution.ArtifactRef, error) {
	ref := evolution.ArtifactRef{ID: id, GroupID: group, SchemaDigest: DatasetSchema, Digest: evolution.Digest(body), Size: int64(len(body)), MediaType: mediaType, GrantID: "dataset-" + id, PolicyRevision: 1}
	if int64(len(body)) > MaxBrowserDatasetBytes || len(body) == 0 || len(id) > 64 || !manifest.ValidID(id) {
		return ref, fmt.Errorf("%w: dataset must be 1 byte to 4 MiB with a short ID", ErrConflict)
	}
	if err := ref.Validate(); err != nil {
		return ref, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return ref, s.SealArtifact(ctx, ref, bytes.NewReader(body))
}
