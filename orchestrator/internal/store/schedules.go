package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AgentSchedule struct {
	ID                  string     `json:"id"`
	UserID              string     `json:"user_id"`
	Name                string     `json:"name"`
	Prompt              string     `json:"prompt"`
	CronExpr            string     `json:"cron_expr,omitempty"`
	Timezone            string     `json:"timezone"`
	Enabled             bool       `json:"enabled"`
	Mode                string     `json:"mode"`
	Archived            bool       `json:"archived"`
	ResultMode          string     `json:"result_mode"`
	TargetSessionID     string     `json:"target_session_id,omitempty"`
	TimeoutSeconds      int        `json:"timeout_seconds"`
	MaxRetries          int        `json:"max_retries"`
	RetryBackoffSeconds int        `json:"retry_backoff_seconds"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	LastRunAt           *time.Time `json:"last_run_at,omitempty"`
	Version             int        `json:"version"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type ScheduleRun struct {
	ID            string     `json:"id"`
	ScheduleID    string     `json:"schedule_id"`
	UserID        string     `json:"user_id"`
	SessionID     string     `json:"session_id,omitempty"`
	TurnID        *int64     `json:"turn_id,omitempty"`
	ScheduledAt   time.Time  `json:"scheduled_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Status        string     `json:"status"`
	Attempt       int        `json:"attempt"`
	ErrorClass    string     `json:"error_class,omitempty"`
	Error         string     `json:"error,omitempty"`
	OutputSummary string     `json:"output_summary,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func scanSchedule(row interface{ Scan(...any) error }) (AgentSchedule, error) {
	var s AgentSchedule
	var enabled, archived int
	var next, last, created, updated, target sql.NullString
	err := row.Scan(&s.ID, &s.UserID, &s.Name, &s.Prompt, &s.CronExpr, &s.Timezone, &enabled, &s.Mode, &archived, &s.ResultMode, &target, &s.TimeoutSeconds, &s.MaxRetries, &s.RetryBackoffSeconds, &next, &last, &s.Version, &created, &updated)
	if err != nil {
		return s, err
	}
	s.Enabled = enabled != 0
	s.Archived = archived != 0
	s.TargetSessionID = target.String
	if s.ResultMode == "" {
		s.ResultMode = "new_session"
	}
	if next.Valid {
		t := parseTime(next.String)
		s.NextRunAt = &t
	}
	if last.Valid {
		t := parseTime(last.String)
		s.LastRunAt = &t
	}
	s.CreatedAt = parseTime(created.String)
	s.UpdatedAt = parseTime(updated.String)
	return s, nil
}

func (s *Store) CreateSchedule(ctx context.Context, schedule AgentSchedule) error {
	if schedule.ID == "" || schedule.UserID == "" || strings.TrimSpace(schedule.Name) == "" || strings.TrimSpace(schedule.Prompt) == "" {
		return errors.New("schedule id, user, name and prompt are required")
	}
	if schedule.Timezone == "" {
		schedule.Timezone = "UTC"
	}
	if schedule.Mode == "" {
		schedule.Mode = "agent"
	}
	if schedule.ResultMode == "" {
		schedule.ResultMode = "new_session"
	}
	if schedule.ResultMode != "new_session" && schedule.ResultMode != "existing_session" {
		return errors.New("invalid schedule result mode")
	}
	if schedule.ResultMode == "existing_session" && strings.TrimSpace(schedule.TargetSessionID) == "" {
		return errors.New("target session is required")
	}
	if schedule.TimeoutSeconds <= 0 {
		schedule.TimeoutSeconds = 1800
	}
	if schedule.Version <= 0 {
		schedule.Version = 1
	}
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = time.Now().UTC()
	}
	if schedule.UpdatedAt.IsZero() {
		schedule.UpdatedAt = schedule.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_schedules (id,user_id,name,prompt,cron_expr,timezone,enabled,mode,archived,result_mode,target_session_id,timeout_seconds,max_retries,retry_backoff_seconds,next_run_at,last_run_at,version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, schedule.ID, schedule.UserID, schedule.Name, schedule.Prompt, schedule.CronExpr, schedule.Timezone, boolInt(schedule.Enabled), schedule.Mode, boolInt(schedule.Archived), schedule.ResultMode, nullableString(schedule.TargetSessionID), schedule.TimeoutSeconds, schedule.MaxRetries, schedule.RetryBackoffSeconds, formatOptionalTime(schedule.NextRunAt), formatOptionalTime(schedule.LastRunAt), schedule.Version, formatTime(schedule.CreatedAt), formatTime(schedule.UpdatedAt))
	return err
}

func (s *Store) GetSchedule(ctx context.Context, id string) (AgentSchedule, error) {
	return scanSchedule(s.db.QueryRowContext(ctx, `SELECT id,user_id,name,prompt,cron_expr,timezone,enabled,mode,archived,result_mode,target_session_id,timeout_seconds,max_retries,retry_backoff_seconds,next_run_at,last_run_at,version,created_at,updated_at FROM agent_schedules WHERE id = ?`, id))
}

func (s *Store) ListSchedulesByUser(ctx context.Context, userID string) ([]AgentSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,name,prompt,cron_expr,timezone,enabled,mode,archived,result_mode,target_session_id,timeout_seconds,max_retries,retry_backoff_seconds,next_run_at,last_run_at,version,created_at,updated_at FROM agent_schedules WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentSchedule{}
	for rows.Next() {
		v, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]AgentSchedule, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,name,prompt,cron_expr,timezone,enabled,mode,archived,result_mode,target_session_id,timeout_seconds,max_retries,retry_backoff_seconds,next_run_at,last_run_at,version,created_at,updated_at FROM agent_schedules WHERE enabled=1 AND archived=0 AND cron_expr<>'' AND next_run_at IS NOT NULL AND next_run_at <= ? ORDER BY next_run_at LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentSchedule{}
	for rows.Next() {
		v, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AdvanceSchedule(ctx context.Context, id string, expectedVersion int, next, last time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_schedules SET next_run_at=?,last_run_at=?,version=version+1,updated_at=? WHERE id=? AND version=? AND enabled=1 AND archived=0`, formatTime(next), formatTime(last), formatTime(time.Now().UTC()), id, expectedVersion)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("schedule claim conflict")
	}
	return nil
}

// ClaimScheduleDue atomically advances a schedule and creates its run row.
func (s *Store) ClaimScheduleDue(ctx context.Context, scheduleID string, expectedVersion int, next, scheduledAt time.Time, run ScheduleRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE agent_schedules SET next_run_at=?,last_run_at=?,version=version+1,updated_at=? WHERE id=? AND version=? AND enabled=1 AND archived=0`, formatTime(next), formatTime(scheduledAt), formatTime(time.Now().UTC()), scheduleID, expectedVersion)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("schedule claim conflict")
	}
	if run.Status == "" {
		run.Status = "queued"
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs (id,schedule_id,user_id,scheduled_at,status,attempt,created_at) VALUES (?,?,?,?,?,?,?)`, run.ID, run.ScheduleID, run.UserID, formatTime(run.ScheduledAt), run.Status, run.Attempt, formatTime(run.CreatedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateSchedule(ctx context.Context, schedule AgentSchedule, expectedVersion int) error {
	if expectedVersion <= 0 {
		expectedVersion = schedule.Version
	}
	if schedule.ResultMode == "" {
		schedule.ResultMode = "new_session"
	}
	if schedule.ResultMode != "new_session" && schedule.ResultMode != "existing_session" {
		return errors.New("invalid schedule result mode")
	}
	now := schedule.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_schedules SET name=?,prompt=?,cron_expr=?,timezone=?,enabled=?,archived=?,result_mode=?,target_session_id=?,timeout_seconds=?,max_retries=?,retry_backoff_seconds=?,next_run_at=?,last_run_at=?,version=version+1,updated_at=? WHERE id=? AND user_id=? AND version=?`, schedule.Name, schedule.Prompt, schedule.CronExpr, schedule.Timezone, boolInt(schedule.Enabled), boolInt(schedule.Archived), schedule.ResultMode, nullableString(schedule.TargetSessionID), schedule.TimeoutSeconds, schedule.MaxRetries, schedule.RetryBackoffSeconds, formatOptionalTime(schedule.NextRunAt), formatOptionalTime(schedule.LastRunAt), formatTime(now), schedule.ID, schedule.UserID, expectedVersion)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("schedule version conflict")
	}
	return nil
}
func (s *Store) DeleteSchedule(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_schedules WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateScheduleRun(ctx context.Context, run ScheduleRun) error {
	if run.Status == "" {
		run.Status = "queued"
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.Attempt < 0 {
		run.Attempt = 0
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedule_runs (id,schedule_id,user_id,session_id,turn_id,scheduled_at,status,attempt,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, run.ID, run.ScheduleID, run.UserID, nullableString(run.SessionID), nullableInt64(run.TurnID), formatTime(run.ScheduledAt), run.Status, run.Attempt, formatTime(run.CreatedAt))
	return err
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func (s *Store) ListScheduleRuns(ctx context.Context, scheduleID, userID string, limit int) ([]ScheduleRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,schedule_id,user_id,session_id,turn_id,scheduled_at,started_at,finished_at,status,attempt,error_class,error,output_summary,created_at FROM schedule_runs WHERE schedule_id=? AND user_id=? ORDER BY scheduled_at DESC,attempt DESC LIMIT ?`, scheduleID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleRun{}
	for rows.Next() {
		var r ScheduleRun
		var scheduled, created string
		var sid, started, finished, ec, er, summary sql.NullString
		var turn sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.UserID, &sid, &turn, &scheduled, &started, &finished, &r.Status, &r.Attempt, &ec, &er, &summary, &created); err != nil {
			return nil, err
		}
		r.SessionID = sid.String
		r.ErrorClass = ec.String
		r.Error = er.String
		r.OutputSummary = summary.String
		if turn.Valid {
			v := turn.Int64
			r.TurnID = &v
		}
		r.ScheduledAt = parseTime(scheduled)
		if started.Valid {
			v := parseTime(started.String)
			r.StartedAt = &v
		}
		if finished.Valid {
			v := parseTime(finished.String)
			r.FinishedAt = &v
		}
		r.CreatedAt = parseTime(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetScheduleRun(ctx context.Context, id, userID string) (ScheduleRun, error) {
	var r ScheduleRun
	var scheduled, created string
	var started, finished sql.NullString
	var sidN, ecN, errN, summary sql.NullString
	var turn sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,schedule_id,user_id,session_id,turn_id,scheduled_at,started_at,finished_at,status,attempt,error_class,error,output_summary,created_at FROM schedule_runs WHERE id=? AND user_id=?`, id, userID).Scan(&r.ID, &r.ScheduleID, &r.UserID, &sidN, &turn, &scheduled, &started, &finished, &r.Status, &r.Attempt, &ecN, &errN, &summary, &created)
	if err != nil {
		return r, err
	}
	if sidN.Valid {
		r.SessionID = sidN.String
	}
	r.ErrorClass = ecN.String
	r.Error = errN.String
	r.OutputSummary = summary.String
	if turn.Valid {
		v := turn.Int64
		r.TurnID = &v
	}
	r.ScheduledAt = parseTime(scheduled)
	if started.Valid {
		v := parseTime(started.String)
		r.StartedAt = &v
	}
	if finished.Valid {
		v := parseTime(finished.String)
		r.FinishedAt = &v
	}
	r.CreatedAt = parseTime(created)
	return r, nil
}
func (s *Store) ListQueuedScheduleRuns(ctx context.Context, now time.Time, limit int) ([]ScheduleRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,schedule_id,user_id,scheduled_at,status,attempt,created_at FROM schedule_runs WHERE status='queued' AND scheduled_at <= ? ORDER BY scheduled_at LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleRun{}
	for rows.Next() {
		var r ScheduleRun
		var scheduled, created string
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.UserID, &scheduled, &r.Status, &r.Attempt, &created); err != nil {
			return nil, err
		}
		r.ScheduledAt = parseTime(scheduled)
		r.CreatedAt = parseTime(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) StartScheduleRun(ctx context.Context, id, sessionID string, turnID int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schedule_runs SET status='running',session_id=?,turn_id=?,started_at=? WHERE id=? AND status='queued'`, sessionID, turnID, formatTime(now), id)
	return err
}
func (s *Store) FinishScheduleRun(ctx context.Context, id, status, errorClass, runErr, summary string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schedule_runs SET status=?,error_class=?,error=?,output_summary=?,finished_at=? WHERE id=? AND status IN ('queued','running')`, status, nullableString(errorClass), nullableString(runErr), summary, formatTime(now), id)
	return err
}
func (s *Store) TurnStatus(ctx context.Context, sessionID string, turnID int64) (string, string, error) {
	var status, errMsg sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status,error FROM turns WHERE id=? AND session_id=?`, turnID, sessionID).Scan(&status, &errMsg)
	return status.String, errMsg.String, err
}
func (s *Store) LatestAssistantMessage(ctx context.Context, sessionID string) (string, error) {
	var content string
	err := s.db.QueryRowContext(ctx, `SELECT content FROM messages WHERE session_id=? AND role='assistant' ORDER BY id DESC LIMIT 1`, sessionID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}
