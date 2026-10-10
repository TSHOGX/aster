package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type ScheduleOverview struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	SessionID       string `json:"session_id,omitempty"`
	Title           string `json:"title"`
	Cron            string `json:"cron"`
	Timezone        string `json:"timezone"`
	State           string `json:"state"`
	TriggerKind     string `json:"trigger_kind"`
	RunAt           string `json:"run_at,omitempty"`
	EndReason       string `json:"end_reason,omitempty"`
	OriginSessionID string `json:"origin_session_id,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	NextAt          string `json:"next_at,omitempty"`
	Revision        int64  `json:"revision"`
	ProjectTitle    string `json:"project_title"`
}
type SchedulePage struct {
	Schedules []ScheduleOverview `json:"schedules"`
	Next      string             `json:"next,omitempty"`
	Previous  string             `json:"previous,omitempty"`
}
type ScheduleFilter struct {
	Project, Query, Status, After, Before, Anchor string
	Limit                                         int
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
	if (f.After != "" && f.Before != "") || (f.Anchor != "" && (f.After != "" || f.Before != "")) || len(f.Anchor) > 128 || len(f.Before) > 2048 || len(f.Query) > 256 || (f.Status != "" && f.Status != "active" && f.Status != "paused" && f.Status != "exhausted" && f.Status != "canceled" && f.Status != "deleted") || len(f.After) > 2048 {
		return out, ErrInvalid
	}
	if f.Limit < 1 || f.Limit > 50 {
		f.Limit = 50
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(actor+"\x00"+f.Project+"\x00"+strings.ToLower(f.Query)+"\x00"+f.Status)))
	cursor := scheduleCursor{Version: 1, Filter: key, Rank: -1}
	continuation := f.After
	if f.Before != "" {
		continuation = f.Before
	}
	if continuation != "" {
		raw, err := base64.RawURLEncoding.DecodeString(continuation)
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
	if continuation != "" && cursor.Revision != revision {
		return out, ErrResync
	}
	const rank = `CASE WHEN s.state<>'active' THEN 2 WHEN s.trigger_kind='manual' THEN 1 ELSE 0 END`
	if f.Anchor != "" {
		err = tx.QueryRowContext(ctx, `SELECT `+rank+`,coalesce(s.next_at,''),s.id FROM schedules s JOIN projects p ON p.id=s.project_id WHERE s.id=? AND p.owner_id=? AND p.state NOT IN ('deleting','deleted') AND (s.state<>'deleted' OR ?='deleted') AND (?='' OR s.project_id=?) AND instr(batterygpt_lower(s.title),batterygpt_lower(?))>0 AND ((?='' AND s.state IN ('active','paused')) OR s.state=?)`, f.Anchor, actor, f.Status, f.Project, f.Project, f.Query, f.Status, f.Status).Scan(&cursor.Rank, &cursor.Next, &cursor.ID)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if err == sql.ErrNoRows {
			cursor.Rank = -1
			cursor.Next = ""
			cursor.ID = ""
		}
	}
	comparison, ordering := ">", "ASC"
	if f.Anchor != "" {
		comparison = ">="
	}
	if f.Before != "" {
		comparison = "<"
		ordering = "DESC"
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.project_id,coalesce(s.session_id,''),s.title,s.cron,s.timezone,s.state,s.trigger_kind,coalesce(s.run_at,''),coalesce(s.end_reason,''),coalesce(s.origin_session_id,''),s.last_error,coalesce(s.next_at,''),s.revision,p.title FROM schedules s JOIN projects p ON p.id=s.project_id
 WHERE p.owner_id=? AND p.state NOT IN ('deleting','deleted') AND (s.state<>'deleted' OR ?='deleted')
 AND (?='' OR s.project_id=?) AND instr(batterygpt_lower(s.title),batterygpt_lower(?))>0
 AND ((?='' AND s.state IN ('active','paused')) OR s.state=?)
 AND (`+rank+`,coalesce(s.next_at,''),s.id)`+comparison+`(?,?,?)
 ORDER BY `+rank+` `+ordering+`,coalesce(s.next_at,'') `+ordering+`,s.id `+ordering+` LIMIT ?`, actor, f.Status, f.Project, f.Project, f.Query, f.Status, f.Status, cursor.Rank, cursor.Next, cursor.ID, f.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ScheduleOverview
		if err = rows.Scan(&item.ID, &item.ProjectID, &item.SessionID, &item.Title, &item.Cron, &item.Timezone, &item.State, &item.TriggerKind, &item.RunAt, &item.EndReason, &item.OriginSessionID, &item.LastError, &item.NextAt, &item.Revision, &item.ProjectTitle); err != nil {
			return out, err
		}
		out.Schedules = append(out.Schedules, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	more := len(out.Schedules) > f.Limit
	if more {
		out.Schedules = out.Schedules[:f.Limit]
	}
	if f.Before != "" {
		slices.Reverse(out.Schedules)
	}
	token := func(item ScheduleOverview) string {
		r := 0
		if item.State != "active" {
			r = 2
		} else if item.TriggerKind == "manual" {
			r = 1
		}
		raw, _ := json.Marshal(scheduleCursor{Version: 1, Revision: revision, Filter: key, Rank: r, Next: item.NextAt, ID: item.ID})
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	if len(out.Schedules) > 0 {
		if more && f.Before == "" || f.Before != "" {
			out.Next = token(out.Schedules[len(out.Schedules)-1])
		}
		if more && f.Before != "" {
			out.Previous = token(out.Schedules[0])
		} else if f.Before == "" && (f.After != "" || cursor.Rank >= 0) {
			first := out.Schedules[0]
			r := 0
			if first.State != "active" {
				r = 2
			} else if first.TriggerKind == "manual" {
				r = 1
			}
			var previous bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedules s JOIN projects p ON p.id=s.project_id WHERE p.owner_id=? AND p.state NOT IN ('deleting','deleted') AND (s.state<>'deleted' OR ?='deleted') AND (?='' OR s.project_id=?) AND instr(batterygpt_lower(s.title),batterygpt_lower(?))>0 AND ((?='' AND s.state IN ('active','paused')) OR s.state=?) AND (`+rank+`,coalesce(s.next_at,''),s.id)<(?,?,?))`, actor, f.Status, f.Project, f.Project, f.Query, f.Status, f.Status, r, first.NextAt, first.ID).Scan(&previous)
			if err != nil {
				return out, err
			}
			if previous {
				out.Previous = token(first)
			}
		}
	}
	return out, nil
}
