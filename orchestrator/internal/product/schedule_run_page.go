package product

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"
)

type ScheduleRunPage struct {
	Runs     []ScheduleRun `json:"runs"`
	Next     string        `json:"next"`
	Previous string        `json:"previous,omitempty"`
}
type scheduleRunCursor struct {
	Version                               int
	Owner, Project, Schedule, Created, ID string
}

// Continuations carry their ordering tuple so deleting the boundary run does
// not silently end history. An anchor relocates a bounded live window.
func (s *Store) ScheduleRunPage(ctx context.Context, scope Scope, id, after, before, anchor string, limit int) (ScheduleRunPage, error) {
	out := ScheduleRunPage{Runs: []ScheduleRun{}}
	if len(after) > 2048 || len(before) > 2048 || len(anchor) > 128 || after != "" && before != "" || anchor != "" && (after != "" || before != "") {
		return out, ErrInvalid
	}
	if _, err := s.Schedule(ctx, scope, id); err != nil {
		return out, err
	}
	if limit < 1 || limit > 50 {
		limit = 50
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	cursor := scheduleRunCursor{Version: 1, Owner: scope.Actor, Project: scope.ProjectID, Schedule: id}
	token := after
	if before != "" {
		token = before
	}
	if token != "" {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || json.Unmarshal(raw, &cursor) != nil {
			return out, ErrInvalid
		} else if cursor.Version != 1 || cursor.Owner != scope.Actor || cursor.Project != scope.ProjectID || cursor.Schedule != id {
			return out, ErrInvalid
		}
	}
	if anchor != "" {
		err := tx.QueryRowContext(ctx, `SELECT created_at,id FROM schedule_runs WHERE project_id=? AND schedule_id=? AND id=?`, scope.ProjectID, id, anchor).Scan(&cursor.Created, &cursor.ID)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
	}
	comparator, order := "<", "DESC"
	if anchor != "" {
		comparator = "<="
	}
	if before != "" {
		comparator = ">"
		order = "ASC"
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.project_id=? AND r.schedule_id=? AND (?='' OR (r.created_at,r.id)`+comparator+`(?,?)) ORDER BY r.created_at `+order+`,r.id `+order+` LIMIT ?`, scope.ProjectID, id, cursor.Created, cursor.Created, cursor.ID, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanScheduleRun(rows)
		if err != nil {
			return out, err
		}
		out.Runs = append(out.Runs, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	more := len(out.Runs) > limit
	if more {
		out.Runs = out.Runs[:limit]
	}
	if before != "" {
		slices.Reverse(out.Runs)
	}
	// Close the read before querying boundaries on the single reader connection.
	rows.Close()
	encode := func(run ScheduleRun) (string, error) {
		c := scheduleRunCursor{Version: 1, Owner: scope.Actor, Project: scope.ProjectID, Schedule: id, ID: run.ID}
		if err := tx.QueryRowContext(ctx, `SELECT created_at FROM schedule_runs WHERE id=? AND schedule_id=?`, run.ID, id).Scan(&c.Created); err != nil {
			return "", err
		}
		raw, err := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(raw), err
	}
	if len(out.Runs) > 0 {
		if more && before == "" || before != "" {
			out.Next, err = encode(out.Runs[len(out.Runs)-1])
			if err != nil {
				return out, err
			}
		}
		previous := more && before != ""
		if before == "" && cursor.Created != "" {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_runs WHERE project_id=? AND schedule_id=? AND (created_at,id)>(SELECT created_at,id FROM schedule_runs WHERE id=? AND schedule_id=?))`, scope.ProjectID, id, out.Runs[0].ID, id).Scan(&previous)
			if err != nil {
				return out, err
			}
		}
		if previous {
			out.Previous, err = encode(out.Runs[0])
			if err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
