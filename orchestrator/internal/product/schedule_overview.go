package product

import "context"

type ScheduleOverview struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	SessionID    string `json:"session_id,omitempty"`
	Title        string `json:"title"`
	Timezone     string `json:"timezone"`
	Enabled      bool   `json:"enabled"`
	NextAt       string `json:"next_at,omitempty"`
	Revision     int64  `json:"revision"`
	ProjectTitle string `json:"project_title"`
}
type SchedulePage struct {
	Schedules []ScheduleOverview `json:"schedules"`
	Next      string             `json:"next,omitempty"`
}

// ScheduleOverviewPage authorizes before pagination: neither a foreign project
// filter nor a shared conversation grants access to its owner's schedules.
func (s *Store) ScheduleOverviewPage(ctx context.Context, actor, project, after string, limit int) (SchedulePage, error) {
	out := SchedulePage{Schedules: []ScheduleOverview{}}
	if actor == "" {
		return out, ErrNotFound
	}
	limit = pageSize(limit)
	rows, err := s.reader.QueryContext(ctx, `SELECT s.id,s.project_id,coalesce(s.session_id,''),s.title,s.timezone,s.enabled,coalesce(s.next_at,''),s.revision,p.title FROM schedules s JOIN projects p ON p.id=s.project_id WHERE p.owner_id=? AND p.state NOT IN ('deleting','deleted') AND s.deleted=0 AND (?='' OR s.project_id=?) AND s.id>? ORDER BY s.id LIMIT ?`, actor, project, project, after, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ScheduleOverview
		if err = rows.Scan(&item.ID, &item.ProjectID, &item.SessionID, &item.Title, &item.Timezone, &item.Enabled, &item.NextAt, &item.Revision, &item.ProjectTitle); err != nil {
			return out, err
		}
		out.Schedules = append(out.Schedules, item)
	}
	if len(out.Schedules) > limit {
		out.Schedules = out.Schedules[:limit]
		out.Next = out.Schedules[limit-1].ID
	}
	return out, rows.Err()
}
