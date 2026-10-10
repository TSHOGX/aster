package product

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"harness-platform/orchestrator/internal/sqlitex"
)

type Schedule struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	Cron      string `json:"cron"`
	Timezone  string `json:"timezone"`
	Enabled   bool   `json:"enabled"`
	Deleted   bool   `json:"deleted"`
	NextAt    string `json:"next_at,omitempty"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
}

const scheduleColumns = `id,project_id,coalesce(session_id,''),title,prompt,cron,timezone,enabled,deleted,coalesce(next_at,''),revision,created_at`

func scanSchedule(row scanner) (s Schedule, err error) {
	err = missing(row.Scan(&s.ID, &s.ProjectID, &s.SessionID, &s.Title, &s.Prompt, &s.Cron, &s.Timezone, &s.Enabled, &s.Deleted, &s.NextAt, &s.Revision, &s.CreatedAt))
	return
}
func parseRecurrence(expression, zone string) (cron.Schedule, string, error) {
	if !validText(zone, 128) || strings.ContainsAny(zone, " \t\n") {
		return nil, "timezone", ErrInvalid
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return nil, "timezone", ErrInvalid
	}
	if expression == "" {
		return nil, "", nil
	}
	if strings.HasPrefix(expression, "@every") {
		minutes := strings.TrimSuffix(strings.TrimPrefix(expression, "@every "), "m")
		n, err := strconv.Atoi(minutes)
		if err != nil || n < 1 || n > MaxIntervalMinutes || expression != "@every "+strconv.Itoa(n)+"m" {
			return nil, "interval", ErrInvalid
		}
		return minuteInterval(time.Duration(n) * time.Minute), "", nil
	}
	if len(expression) > 128 || len(strings.Fields(expression)) != 5 {
		return nil, "cron", ErrInvalid
	}
	parsed, err := cron.ParseStandard("CRON_TZ=" + zone + " " + expression)
	if err != nil {
		return nil, "cron", ErrInvalid
	}
	return parsed, "", nil
}
func scheduleNext(s Schedule, now time.Time) (string, error) {
	parsed, _, err := parseRecurrence(s.Cron, s.Timezone)
	if err != nil {
		return "", err
	}
	if parsed == nil {
		return "", nil
	}
	next := parsed.Next(now)
	if interval, ok := parsed.(minuteInterval); ok && s.NextAt != "" {
		anchor, err := time.Parse(time.RFC3339Nano, s.NextAt)
		if err != nil {
			return "", ErrInvalid
		}
		next = interval.after(now, anchor)
	}
	if next.IsZero() {
		return "", ErrInvalid
	}
	if !s.Enabled {
		return "", nil
	}
	return sqlitex.FormatTime(next), nil
}
func validSchedule(s Schedule) bool {
	if _, err := nativePayload(map[string]string{"type": "prompt", "message": s.Prompt}); err != nil {
		return false
	}
	return validID(s.ID) && validText(s.Title, 256) && validText(s.Prompt, 1<<20) && (s.SessionID == "" || validID(s.SessionID)) && validText(s.Timezone, 128)
}
func (s *Store) CreateSchedule(ctx context.Context, scope Scope, input Schedule) (Schedule, error) {
	input.NextAt = "" // Server-owned; creation always starts a new cadence.
	if !validSchedule(input) {
		return Schedule{}, ErrInvalid
	}
	next, err := scheduleNext(input, s.now())
	if err != nil {
		return Schedule{}, err
	}
	var out Schedule
	err = s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		digest := creationDigest(input.SessionID, input.Title, input.Prompt, input.Cron, input.Timezone, input.Enabled)
		old, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, input.ID))
		if e == nil {
			if old.ProjectID != scope.ProjectID {
				return ErrNotFound
			}
			var stored string
			if e = tx.QueryRowContext(ctx, `SELECT creation_digest FROM schedules WHERE id=?`, input.ID).Scan(&stored); e != nil {
				return e
			}
			if stored != digest {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if input.SessionID != "" {
			child, e := sessionIn(ctx, tx, scope, input.SessionID)
			if e != nil {
				return e
			}
			if child.Deletion != Live {
				return ErrDeleting
			}
		}
		now := s.timestamp()
		if _, e = tx.ExecContext(ctx, `INSERT INTO schedules(id,project_id,session_id,title,prompt,cron,timezone,enabled,next_at,creation_digest,created_at,updated_at) VALUES(?,?,nullif(?,''),?,?,?,?,?,nullif(?,''),?,?,?)`, input.ID, scope.ProjectID, input.SessionID, input.Title, input.Prompt, input.Cron, input.Timezone, input.Enabled, next, digest, now, now); e != nil {
			return e
		}
		if e = s.bumpProject(ctx, tx, scope.ProjectID, "schedule.created"); e != nil {
			return e
		}
		out, e = scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, input.ID))
		return e
	})
	return out, err
}
func (s *Store) Schedules(ctx context.Context, scope Scope, after string, limit int) ([]Schedule, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE project_id=? AND id>? AND deleted=0 ORDER BY id LIMIT ?`, scope.ProjectID, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		v, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) UpdateSchedule(ctx context.Context, scope Scope, input Schedule, remove bool) error {
	input.NextAt = "" // Never accept a client-supplied execution deadline.
	if !validSchedule(input) {
		return ErrInvalid
	}
	next, err := scheduleNext(input, s.now())
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		old, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, input.ID, scope.ProjectID))
		if err != nil {
			return err
		}
		if old.Deleted || old.Revision != input.Revision {
			return ErrConflict
		}
		parsed, _, _ := parseRecurrence(input.Cron, input.Timezone)
		_, interval := parsed.(minuteInterval)
		if input.Cron == old.Cron && input.Enabled == old.Enabled && (input.Timezone == old.Timezone || interval) {
			// Content/target edits (and interval display-zone edits) must not
			// postpone the next run, including an already overdue occurrence.
			next = old.NextAt
		}
		if input.SessionID != "" {
			child, e := sessionIn(ctx, tx, scope, input.SessionID)
			if e != nil {
				return e
			}
			if child.Deletion != Live {
				return ErrDeleting
			}
		}
		if remove {
			input.Enabled = false
			next = ""
		}
		if err = changed(tx.ExecContext(ctx, `UPDATE schedules SET session_id=nullif(?,''),title=?,prompt=?,cron=?,timezone=?,enabled=?,next_at=nullif(?,''),deleted=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, input.SessionID, input.Title, input.Prompt, input.Cron, input.Timezone, input.Enabled, next, remove, s.timestamp(), input.ID, input.Revision)); err != nil {
			return err
		}
		return s.bumpProject(ctx, tx, scope.ProjectID, "schedule.updated")
	})
}

type ScheduleRun struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	TurnID     string `json:"turn_id"`
	RequestID  string `json:"request_id"`
	DueAt      string `json:"due_at"`
	State      string `json:"state"`
}

// Schedule runs display the same authoritative prompt status as the queue and
// conversation controls, including claims before a delivery is consumed.
var runColumns = `r.id,r.schedule_id,r.project_id,r.session_id,r.turn_id,r.request_id,r.due_at,` + strings.ReplaceAll(promptStatus, "turns.", "t.")

func scanScheduleRun(row scanner) (r ScheduleRun, err error) {
	err = missing(row.Scan(&r.ID, &r.ScheduleID, &r.ProjectID, &r.SessionID, &r.TurnID, &r.RequestID, &r.DueAt, &r.State))
	return
}
func (s *Store) RunSchedule(ctx context.Context, scope Scope, id, request string) (ScheduleRun, error) {
	if !validText(request, 128) {
		return ScheduleRun{}, ErrInvalid
	}
	var run ScheduleRun
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		schedule, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, id, scope.ProjectID))
		if err != nil {
			return err
		}
		run, err = s.runSchedule(ctx, tx, scope, schedule, "manual:"+request, "")
		return err
	})
	return run, err
}
func (s *Store) runSchedule(ctx context.Context, tx *sql.Tx, scope Scope, schedule Schedule, request, due string) (ScheduleRun, error) {
	old, err := scanScheduleRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.schedule_id=? AND r.request_id=?`, schedule.ID, request))
	if err == nil {
		return old, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ScheduleRun{}, err
	}
	if schedule.Deleted {
		return ScheduleRun{}, ErrDeleting
	}
	if !schedule.Enabled {
		return ScheduleRun{}, ErrConflict
	}
	id := uuid.NewString()
	session := schedule.SessionID
	if session == "" {
		session = uuid.NewString()
		if _, err = s.createSession(ctx, tx, scope, session, schedule.Title); err != nil {
			return ScheduleRun{}, err
		}
	}
	turn, err := s.admitTurn(ctx, tx, scope, session, "schedule:"+id, schedule.Prompt, false)
	if err != nil {
		return ScheduleRun{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs(id,schedule_id,project_id,session_id,turn_id,request_id,due_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, schedule.ID, scope.ProjectID, session, turn.ID, request, due, s.timestamp()); err != nil {
		return ScheduleRun{}, err
	}
	return ScheduleRun{ID: id, ScheduleID: schedule.ID, ProjectID: scope.ProjectID, SessionID: session, TurnID: turn.ID, RequestID: request, DueAt: due, State: turn.State}, nil
}
func (s *Store) ScheduleRuns(ctx context.Context, scope Scope, id, after string, limit int) ([]ScheduleRun, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+runColumns+` FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.project_id=? AND r.schedule_id=? AND (?='' OR (r.created_at,r.id)<(SELECT created_at,id FROM schedule_runs WHERE id=? AND schedule_id=?)) ORDER BY r.created_at DESC,r.id DESC LIMIT ?`, scope.ProjectID, id, after, after, id, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleRun{}
	for rows.Next() {
		r, err := scanScheduleRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TickSchedules coalesces missed occurrences and shares foreground admission.
// It owns no sandbox release decision and makes no remote call.
func (s *Store) TickSchedules(ctx context.Context, limit int) (int, error) {
	now := s.now()
	rows, err := s.reader.QueryContext(ctx, `SELECT id FROM schedules WHERE enabled=1 AND deleted=0 AND next_at<=? ORDER BY next_at,id LIMIT ?`, sqlitex.FormatTime(now), pageSize(limit))
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		err = s.write(ctx, func(tx *sql.Tx) error {
			schedule, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, id))
			if e != nil {
				return e
			}
			if !schedule.Enabled || schedule.Deleted || schedule.NextAt == "" || schedule.NextAt > sqlitex.FormatTime(now) {
				return nil
			}
			var actor string
			if e = tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=? AND state NOT IN ('deleting','deleted')`, schedule.ProjectID).Scan(&actor); e != nil {
				return missing(e)
			}
			scope := Scope{actor, schedule.ProjectID}
			if _, e = s.runSchedule(ctx, tx, scope, schedule, "scheduled:"+schedule.NextAt, schedule.NextAt); e != nil {
				return e
			}
			next, e := scheduleNext(schedule, now)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE schedules SET next_at=?,revision=revision+1,updated_at=? WHERE id=?`, next, s.timestamp(), id); e != nil {
				return e
			}
			count++
			return nil
		})
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Store) Schedule(ctx context.Context, scope Scope, id string) (Schedule, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return Schedule{}, err
	}
	return scanSchedule(s.reader.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, id, scope.ProjectID))
}
