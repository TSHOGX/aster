package projections

import (
	manifest "aster/contracts/volumemanifest"
	"context"
	"fmt"
	"time"
)

type RunWork struct {
	RunID     string `json:"run_id"`
	GroupID   string `json:"group_id"`
	BindingID string `json:"binding_id"`
	State     string `json:"state"`
}
type RunWorkPage struct {
	Runs      []RunWork `json:"runs"`
	NextAfter string    `json:"next_after"`
	HasMore   bool      `json:"has_more"`
}

// RunnableRuns is service-internal discovery, not a claim or authorization grant.
// A worker must acquire a fresh fenced lease and check current effect authority.
func (s *Store) RunnableRuns(ctx context.Context, after string, limit int) (RunWorkPage, error) {
	result := RunWorkPage{Runs: []RunWork{}, NextAfter: after}
	if (after != "" && !manifest.ValidID(after)) || limit < 1 || limit > 100 {
		return result, fmt.Errorf("invalid work page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,group_id,binding_id,state FROM runs WHERE id>? AND (state='queued' OR (state IN ('running','cancelling') AND lease_until<=?)) ORDER BY id LIMIT ?`, after, time.Now().UnixNano(), limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var value RunWork
		if err = rows.Scan(&value.RunID, &value.GroupID, &value.BindingID, &value.State); err != nil {
			return result, err
		}
		if len(result.Runs) == limit {
			result.HasMore = true
			break
		}
		result.Runs = append(result.Runs, value)
		result.NextAfter = value.RunID
	}
	return result, rows.Err()
}
