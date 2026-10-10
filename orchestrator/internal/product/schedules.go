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
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	SessionID        string `json:"session_id,omitempty"`
	Title            string `json:"title"`
	Prompt           string `json:"prompt"`
	Cron             string `json:"cron"`
	Timezone         string `json:"timezone"`
	State            string `json:"state"`
	TriggerKind      string `json:"trigger_kind"`
	RunAt            string `json:"run_at,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	EndReason        string `json:"end_reason,omitempty"`
	Notify           bool   `json:"notify"`
	RetryAt          string `json:"retry_at,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	OriginSessionID  string `json:"origin_session_id,omitempty"`
	OriginRunID      string `json:"origin_run_id,omitempty"`
	OriginToolCallID string `json:"origin_tool_call_id,omitempty"`
	NextAt           string `json:"next_at,omitempty"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"created_at"`
}

const scheduleColumns = `id,project_id,coalesce(session_id,''),title,prompt,cron,timezone,state,trigger_kind,coalesce(run_at,''),coalesce(expires_at,''),coalesce(end_reason,''),notify,coalesce(retry_at,''),last_error,coalesce(origin_session_id,''),coalesce(origin_run_id,''),coalesce(origin_tool_call_id,''),coalesce(next_at,''),revision,created_at`

func scanSchedule(row scanner) (s Schedule, err error) {
	err = missing(row.Scan(&s.ID, &s.ProjectID, &s.SessionID, &s.Title, &s.Prompt, &s.Cron, &s.Timezone, &s.State, &s.TriggerKind, &s.RunAt, &s.ExpiresAt, &s.EndReason, &s.Notify, &s.RetryAt, &s.LastError, &s.OriginSessionID, &s.OriginRunID, &s.OriginToolCallID, &s.NextAt, &s.Revision, &s.CreatedAt))
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

// normalizeSchedule validates authored trigger fields, never operational projections.
func normalizeSchedule(input Schedule) (Schedule, error) {
	if !validID(input.ID) || !validText(input.Title, 256) || !validText(input.Prompt, 1<<20) || (input.SessionID != "" && !validID(input.SessionID)) {
		return input, ErrInvalid
	}
	if _, err := nativePayload(map[string]string{"type": "prompt", "message": input.Prompt}); err != nil {
		return input, ErrInvalid
	}
	parsed, _, err := parseRecurrence(input.Cron, input.Timezone)
	if err != nil {
		return input, err
	}
	switch input.TriggerKind {
	case "at":
		if input.Cron != "" || input.RunAt == "" {
			return input, ErrInvalid
		}
	case "manual":
		if parsed != nil || input.RunAt != "" {
			return input, ErrInvalid
		}
	case "interval":
		if _, ok := parsed.(minuteInterval); !ok || input.RunAt != "" {
			return input, ErrInvalid
		}
	case "cron":
		if parsed == nil || strings.HasPrefix(input.Cron, "@") || input.RunAt != "" {
			return input, ErrInvalid
		}
	default:
		return input, ErrInvalid
	}
	for _, field := range []*string{&input.RunAt, &input.ExpiresAt} {
		if *field != "" {
			t, e := time.Parse(time.RFC3339Nano, *field)
			if e != nil {
				return input, ErrInvalid
			}
			*field = sqlitex.FormatTime(t)
		}
	}
	if input.ExpiresAt != "" && input.RunAt != "" && input.ExpiresAt <= input.RunAt {
		return input, ErrInvalid
	}
	if input.State != "active" && input.State != "paused" {
		return input, ErrInvalid
	}
	return input, nil
}
func scheduleNext(task Schedule, now time.Time) (string, error) {
	if task.State != "active" {
		return "", nil
	}
	if task.TriggerKind == "at" {
		return task.RunAt, nil
	}
	parsed, _, err := parseRecurrence(task.Cron, task.Timezone)
	if err != nil {
		return "", err
	}
	if parsed == nil {
		return "", nil
	}
	next := parsed.Next(now)
	if interval, ok := parsed.(minuteInterval); ok && task.NextAt != "" {
		anchor, e := time.Parse(time.RFC3339Nano, task.NextAt)
		if e != nil {
			return "", ErrInvalid
		}
		next = interval.after(now, anchor)
	}
	if next.IsZero() {
		return "", ErrInvalid
	}
	return sqlitex.FormatTime(next), nil
}
func (s *Store) CreateSchedule(ctx context.Context, scope Scope, input Schedule) (Schedule, error) {
	receipt, err := s.MutateSchedule(ctx, scope, ScheduleMutation{ID: input.ID, RequestID: creationDigest("create", input.ID), Operation: "create", Patch: input.definitionPatch()})
	return receipt.Schedule, err
}
func (s *Store) createSchedule(ctx context.Context, tx *sql.Tx, scope Scope, input Schedule) (Schedule, error) {
	if _, err := writableProject(ctx, tx, scope); err != nil {
		return Schedule{}, err
	}
	input, err := normalizeSchedule(input)
	if err != nil {
		return Schedule{}, err
	}
	digest := creationDigest(input.SessionID, input.Title, input.Prompt, input.TriggerKind, input.Cron, input.Timezone, input.State, input.RunAt, input.ExpiresAt, input.Notify)
	if input.RunAt != "" && input.RunAt <= s.timestamp() || input.ExpiresAt != "" && input.ExpiresAt <= s.timestamp() {
		return Schedule{}, ErrInvalid
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM schedules WHERE project_id=? AND state IN ('active','paused')`, scope.ProjectID).Scan(&count); err != nil {
		return Schedule{}, err
	}
	if count >= 100 {
		return Schedule{}, ErrCapacity
	}
	if err = validateScheduleTarget(ctx, tx, scope, input.SessionID); err != nil {
		return Schedule{}, err
	}
	input.NextAt = ""
	next, err := scheduleNext(input, s.now())
	if err != nil {
		return Schedule{}, err
	}
	now := s.timestamp()
	_, err = tx.ExecContext(ctx, `INSERT INTO schedules(id,project_id,session_id,title,prompt,cron,timezone,state,trigger_kind,run_at,expires_at,notify,next_at,creation_digest,created_at,updated_at,origin_session_id,origin_run_id,origin_tool_call_id) VALUES(?,?,nullif(?,''),?,?,?,?,?,?,nullif(?,''),nullif(?,''),?,nullif(?,''),?,?,?,nullif(?,''),nullif(?,''),nullif(?,''))`, input.ID, scope.ProjectID, input.SessionID, input.Title, input.Prompt, input.Cron, input.Timezone, input.State, input.TriggerKind, input.RunAt, input.ExpiresAt, input.Notify, next, digest, now, now, input.OriginSessionID, input.OriginRunID, input.OriginToolCallID)
	if err != nil {
		return Schedule{}, err
	}
	if err = s.bumpProject(ctx, tx, scope.ProjectID, "schedule.created"); err != nil {
		return Schedule{}, err
	}
	return scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, input.ID))
}
func validateScheduleTarget(ctx context.Context, tx *sql.Tx, scope Scope, id string) error {
	if id == "" {
		return nil
	}
	child, err := sessionIn(ctx, tx, scope, id)
	if err != nil {
		return err
	}
	if child.Deletion != Live {
		return ErrDeleting
	}
	return nil
}
func (s *Store) Schedules(ctx context.Context, scope Scope, after string, limit int) ([]Schedule, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE project_id=? AND id>? AND state<>'deleted' ORDER BY id LIMIT ?`, scope.ProjectID, after, pageSize(limit))
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
	operation := "update"
	patch := input.definitionPatch()
	if remove {
		operation = "delete"
		patch = SchedulePatch{}
	}
	_, err := s.MutateSchedule(ctx, scope, ScheduleMutation{ID: input.ID, RequestID: creationDigest(operation, input.ID, input.Revision, patch), Operation: operation, Revision: input.Revision, Patch: patch})
	return err
}
func (s *Store) updateSchedule(ctx context.Context, tx *sql.Tx, scope Scope, input Schedule, remove bool) error {
	if _, err := writableProject(ctx, tx, scope); err != nil {
		return err
	}
	old, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, input.ID, scope.ProjectID))
	if err != nil {
		return err
	}
	if old.State == "deleted" || old.Revision != input.Revision {
		return ErrConflict
	}
	if remove {
		return changed(tx.ExecContext(ctx, `UPDATE schedules SET state='deleted',end_reason='user_deleted',next_at=NULL,retry_at=NULL,revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), input.ID))
	}
	if old.State != "active" && old.State != "paused" {
		return ErrConflict
	}
	input, err = normalizeSchedule(input)
	if err != nil {
		return err
	}
	if err = validateScheduleTarget(ctx, tx, scope, input.SessionID); err != nil {
		return err
	}
	input.NextAt = ""
	next, err := scheduleNext(input, s.now())
	if err != nil {
		return err
	}
	sameTrigger := input.TriggerKind == old.TriggerKind && input.Cron == old.Cron && input.RunAt == old.RunAt && (input.Timezone == old.Timezone || input.TriggerKind == "interval" || input.TriggerKind == "at")
	if sameTrigger && input.State == old.State {
		next = old.NextAt
	} else if input.RunAt != "" && input.RunAt <= s.timestamp() {
		return ErrInvalid
	}
	if input.ExpiresAt != "" && input.ExpiresAt <= s.timestamp() {
		return ErrInvalid
	}
	return changed(tx.ExecContext(ctx, `UPDATE schedules SET session_id=nullif(?,''),title=?,prompt=?,cron=?,timezone=?,state=?,trigger_kind=?,run_at=nullif(?,''),expires_at=nullif(?,''),notify=?,next_at=nullif(?,''),retry_at=NULL,retry_count=0,last_error='',end_reason=NULL,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, input.SessionID, input.Title, input.Prompt, input.Cron, input.Timezone, input.State, input.TriggerKind, input.RunAt, input.ExpiresAt, input.Notify, next, s.timestamp(), input.ID, input.Revision))
}
func (s *Store) cancelSchedule(ctx context.Context, tx *sql.Tx, scope Scope, id string, revision int64) error {
	if _, err := writableProject(ctx, tx, scope); err != nil {
		return err
	}
	if _, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, id, scope.ProjectID)); err != nil {
		return err
	}

	return changed(tx.ExecContext(ctx, `UPDATE schedules SET state='canceled',end_reason='user_canceled',next_at=NULL,retry_at=NULL,revision=revision+1,updated_at=? WHERE id=? AND project_id=? AND revision=? AND state IN ('active','paused','exhausted')`, s.timestamp(), id, scope.ProjectID, revision))
}

type ScheduleRun struct {
	ID               string `json:"id"`
	ScheduleID       string `json:"schedule_id"`
	ProjectID        string `json:"project_id"`
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	RequestID        string `json:"request_id"`
	DueAt            string `json:"due_at"`
	State            string `json:"state"`
	ScheduleRevision int64  `json:"schedule_revision"`
	Notify           bool   `json:"notify"`
}

// Schedule runs display the same authoritative prompt status as the queue and
// conversation controls, including claims before a delivery is consumed.
var runColumns = `r.id,r.schedule_id,r.project_id,r.session_id,r.turn_id,r.request_id,r.due_at,r.schedule_revision,r.notify,` + strings.ReplaceAll(promptStatus, "turns.", "t.")

func scanScheduleRun(row scanner) (r ScheduleRun, err error) {
	err = missing(row.Scan(&r.ID, &r.ScheduleID, &r.ProjectID, &r.SessionID, &r.TurnID, &r.RequestID, &r.DueAt, &r.ScheduleRevision, &r.Notify, &r.State))
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
	if schedule.TriggerKind == "at" {
		request = "scheduled:" + schedule.RunAt
		due = schedule.RunAt
	}
	old, err := scanScheduleRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.schedule_id=? AND r.request_id=?`, schedule.ID, request))
	if err == nil {
		return old, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ScheduleRun{}, err
	}
	if schedule.State == "deleted" {
		return ScheduleRun{}, ErrDeleting
	}
	if schedule.State != "active" || schedule.ExpiresAt != "" && schedule.ExpiresAt <= s.timestamp() {
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs(id,schedule_id,project_id,session_id,turn_id,request_id,due_at,created_at,schedule_revision,notify) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, schedule.ID, scope.ProjectID, session, turn.ID, request, due, s.timestamp(), schedule.Revision, schedule.Notify); err != nil {
		return ScheduleRun{}, err
	}
	if schedule.TriggerKind == "at" {
		if _, err = tx.ExecContext(ctx, `UPDATE schedules SET state='exhausted',end_reason='trigger_consumed',next_at=NULL,retry_at=NULL,updated_at=? WHERE id=?`, s.timestamp(), schedule.ID); err != nil {
			return ScheduleRun{}, err
		}
	}
	return ScheduleRun{ID: id, ScheduleID: schedule.ID, ProjectID: scope.ProjectID, SessionID: session, TurnID: turn.ID, RequestID: request, DueAt: due, State: turn.State, ScheduleRevision: schedule.Revision, Notify: schedule.Notify}, nil
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

// TickSchedules uses independent transactions. retry_at removes blocked or bad
// rows from eligibility without changing their original occurrence identity.
func (s *Store) TickSchedules(ctx context.Context, limit int) (int, error) {
	now := s.timestamp()
	rows, err := s.reader.QueryContext(ctx, `SELECT id FROM schedules WHERE state='active' AND (next_at<=? OR expires_at<=?) AND (retry_at IS NULL OR retry_at<=?) ORDER BY coalesce(next_at,expires_at),id LIMIT ?`, now, now, now, pageSize(limit))
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
		admitted := false
		err = s.write(ctx, func(tx *sql.Tx) error {
			task, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, id))
			if e != nil {
				return e
			}
			if task.State != "active" || task.RetryAt > now {
				return nil
			}
			if task.ExpiresAt != "" && task.ExpiresAt <= now {
				_, e = tx.ExecContext(ctx, `UPDATE schedules SET state='exhausted',end_reason='deadline',next_at=NULL,retry_at=NULL,updated_at=? WHERE id=?`, now, id)
				return e
			}
			if task.NextAt == "" || task.NextAt > now {
				return nil
			}
			var outstanding bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.schedule_id=? AND (`+strings.ReplaceAll(promptStatus, "turns.", "t.")+`) IN ('queued','held','claimed','starting','running','stopping','unknown','consumed'))`, id).Scan(&outstanding)
			if e != nil {
				return e
			}
			if outstanding {
				_, e = tx.ExecContext(ctx, `UPDATE schedules SET retry_at=? WHERE id=?`, sqlitex.FormatTime(s.now().Add(30*time.Second)), id)
				return e
			}
			var actor string
			if e = tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=? AND state NOT IN ('deleting','deleted')`, task.ProjectID).Scan(&actor); e != nil {
				return missing(e)
			}
			if _, e = s.runSchedule(ctx, tx, Scope{actor, task.ProjectID}, task, "scheduled:"+task.NextAt, task.NextAt); e != nil {
				return e
			}
			if task.TriggerKind != "at" {
				next, e := scheduleNext(task, s.now())
				if e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE schedules SET next_at=nullif(?,''),retry_at=NULL,retry_count=0,last_error='',updated_at=? WHERE id=?`, next, now, id); e != nil {
					return e
				}
			}
			admitted = true
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			permanent := errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrDeleting)
			// Unexpected database failures are global, not invented task outcomes.
			if !permanent && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrCapacity) && !errors.Is(err, ErrDraining) {
				return count, err
			}
			if e := s.write(ctx, func(tx *sql.Tx) error {
				if permanent {
					_, e := tx.ExecContext(ctx, `UPDATE schedules SET state='paused',next_at=NULL,retry_at=NULL,last_error='Task configuration or target is unavailable',revision=revision+1,updated_at=? WHERE id=? AND state='active'`, now, id)
					return e
				}
				var attempts int
				if e := tx.QueryRowContext(ctx, `SELECT retry_count FROM schedules WHERE id=?`, id).Scan(&attempts); e != nil {
					return e
				}
				delay := 5 * time.Second
				if attempts == 1 {
					delay = 30 * time.Second
				}
				if attempts >= 2 {
					delay = 5 * time.Minute
				}
				_, e := tx.ExecContext(ctx, `UPDATE schedules SET retry_at=?,retry_count=min(retry_count+1,3),last_error='Admission temporarily unavailable',updated_at=? WHERE id=? AND state='active'`, sqlitex.FormatTime(s.now().Add(delay)), now, id)
				return e
			}); e != nil {
				return count, e
			}
			continue
		}
		if admitted {
			count++
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
