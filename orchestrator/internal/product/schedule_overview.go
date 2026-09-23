package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type ScheduleOverview struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	SessionID    string `json:"session_id,omitempty"`
	Title        string `json:"title"`
	Cron         string `json:"cron"`
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
type ScheduleFilter struct {
	Project, Query, Status, After string
	Limit                         int
}
type scheduleCursor struct {
	Version  int
	Revision int64
	Filter   string
	Rank     int
	Next, ID string
}

// ScheduleOverviewPage filters and orders within one read snapshot. Owner-scoped
// catalog epochs reject continuation after writes, rather than omitting tasks
// whose next occurrence moved across the cursor. No prompt bodies are read.
func (s *Store) ScheduleOverviewPage(ctx context.Context, actor string, f ScheduleFilter) (SchedulePage, error) {
	out := SchedulePage{Schedules: []ScheduleOverview{}}
	if actor == "" {
		return out, ErrNotFound
	}
	f.Query = strings.TrimSpace(f.Query)
	if len(f.Query) > 256 || (f.Status != "" && f.Status != "enabled" && f.Status != "paused") || len(f.After) > 2048 {
		return out, ErrInvalid
	}
	if f.Limit < 1 || f.Limit > 50 {
		f.Limit = 50
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(actor+"\x00"+f.Project+"\x00"+strings.ToLower(f.Query)+"\x00"+f.Status)))
	cursor := scheduleCursor{Version: 1, Filter: key, Rank: -1}
	if f.After != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.After)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.Filter != key || cursor.Rank < 0 || cursor.Rank > 2 || !validID(cursor.ID) {
			return out, ErrInvalid
		}
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT revision FROM schedule_catalog WHERE owner_id=?),0)`, actor).Scan(&revision); err != nil {
		return out, err
	}
	if f.After != "" && cursor.Revision != revision {
		return out, ErrResync
	}
	const rank = `CASE WHEN s.enabled=0 THEN 2 WHEN s.cron='' THEN 1 ELSE 0 END`
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.project_id,coalesce(s.session_id,''),s.title,s.cron,s.timezone,s.enabled,coalesce(s.next_at,''),s.revision,p.title FROM schedules s JOIN projects p ON p.id=s.project_id
 WHERE p.owner_id=? AND p.state NOT IN ('deleting','deleted') AND s.deleted=0
 AND (?='' OR s.project_id=?) AND instr(batterygpt_lower(s.title),batterygpt_lower(?))>0
 AND (?='' OR (?='enabled' AND s.enabled=1) OR (?='paused' AND s.enabled=0))
 AND (`+rank+`,coalesce(s.next_at,''),s.id)>(?,?,?)
 ORDER BY `+rank+`,coalesce(s.next_at,''),s.id LIMIT ?`, actor, f.Project, f.Project, f.Query, f.Status, f.Status, f.Status, cursor.Rank, cursor.Next, cursor.ID, f.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ScheduleOverview
		if err = rows.Scan(&item.ID, &item.ProjectID, &item.SessionID, &item.Title, &item.Cron, &item.Timezone, &item.Enabled, &item.NextAt, &item.Revision, &item.ProjectTitle); err != nil {
			return out, err
		}
		out.Schedules = append(out.Schedules, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Schedules) > f.Limit {
		out.Schedules = out.Schedules[:f.Limit]
		last := out.Schedules[f.Limit-1]
		rank := 0
		if !last.Enabled {
			rank = 2
		} else if last.Cron == "" {
			rank = 1
		}
		raw, _ := json.Marshal(scheduleCursor{Version: 1, Revision: revision, Filter: key, Rank: rank, Next: last.NextAt, ID: last.ID})
		out.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}
