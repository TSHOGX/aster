package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

const maxSchedulePromptBytes = 64 << 10

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSchedulesByUser(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": items})
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                string `json:"name"`
		Prompt              string `json:"prompt"`
		CronExpr            string `json:"cron_expr"`
		Timezone            string `json:"timezone"`
		Enabled             *bool  `json:"enabled"`
		TimeoutSeconds      *int   `json:"timeout_seconds"`
		MaxRetries          *int   `json:"max_retries"`
		RetryBackoffSeconds *int   `json:"retry_backoff_seconds"`
		ResultMode          string `json:"result_mode"`
		TargetSessionID     string `json:"target_session_id"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	name, prompt := strings.TrimSpace(req.Name), strings.TrimSpace(req.Prompt)
	if name == "" || prompt == "" {
		writeError(w, http.StatusBadRequest, "name and prompt are required")
		return
	}
	if len(prompt) > maxSchedulePromptBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "prompt too large")
		return
	}
	zone := strings.TrimSpace(req.Timezone)
	if zone == "" {
		zone = "UTC"
	}
	if _, err := time.LoadLocation(zone); err != nil {
		writeError(w, http.StatusBadRequest, "invalid timezone")
		return
	}
	expr := strings.TrimSpace(req.CronExpr)
	var next *time.Time
	if expr != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(expr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cron expression")
			return
		}
		now := time.Now().In(mustLocation(zone))
		t := sched.Next(now).UTC()
		next = &t
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	timeout := 0
	if req.TimeoutSeconds != nil {
		timeout = *req.TimeoutSeconds
	}
	if timeout <= 0 {
		timeout = int(s.cfg.Harness.Scheduler.DefaultTimeout.Seconds())
	}
	if timeout <= 0 {
		timeout = 1800
	}
	maxRetries, backoff := 0, 0
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
	}
	if req.RetryBackoffSeconds != nil {
		backoff = *req.RetryBackoffSeconds
	}
	resultMode := strings.TrimSpace(req.ResultMode)
	if resultMode == "" {
		resultMode = "new_session"
	}
	if resultMode != "new_session" && resultMode != "existing_session" {
		writeError(w, http.StatusBadRequest, "invalid result_mode")
		return
	}
	targetSessionID := strings.TrimSpace(req.TargetSessionID)
	if resultMode == "existing_session" {
		if targetSessionID == "" {
			writeError(w, http.StatusBadRequest, "target_session_id is required for existing_session")
			return
		}
		if !s.scheduleTargetAllowed(r.Context(), userIDFromContext(r.Context()), targetSessionID) {
			writeError(w, http.StatusBadRequest, "target session not found")
			return
		}
	} else {
		targetSessionID = ""
	}
	if maxRetries < 0 || maxRetries > 10 || backoff < 0 {
		writeError(w, http.StatusBadRequest, "invalid retry settings")
		return
	}
	now := time.Now().UTC()
	item := store.AgentSchedule{ID: newID("sched"), UserID: userIDFromContext(r.Context()), Name: name, Prompt: prompt, CronExpr: expr, Timezone: zone, Enabled: enabled, Mode: "agent", ResultMode: resultMode, TargetSessionID: targetSessionID, TimeoutSeconds: timeout, MaxRetries: maxRetries, RetryBackoffSeconds: backoff, NextRunAt: next, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateSchedule(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (s *Server) scheduleRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/schedules/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	user := userIDFromContext(r.Context())
	sched, err := s.store.GetSchedule(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sched.UserID != user {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, sched)
		case http.MethodPatch:
			s.updateSchedule(w, r, sched)
		case http.MethodDelete:
			if err := s.store.DeleteSchedule(r.Context(), id, user); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost {
		if sched.Archived {
			writeError(w, http.StatusConflict, "schedule is archived")
			return
		}
		s.manualScheduleRun(w, r, sched)
		return
	}
	if len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodGet {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		runs, err := s.store.ListScheduleRuns(r.Context(), id, user, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request, current store.AgentSchedule) {
	var req struct {
		Name                *string `json:"name"`
		Prompt              *string `json:"prompt"`
		CronExpr            *string `json:"cron_expr"`
		Timezone            *string `json:"timezone"`
		Enabled             *bool   `json:"enabled"`
		Archived            *bool   `json:"archived"`
		ResultMode          *string `json:"result_mode"`
		TargetSessionID     *string `json:"target_session_id"`
		TimeoutSeconds      *int    `json:"timeout_seconds"`
		MaxRetries          *int    `json:"max_retries"`
		RetryBackoffSeconds *int    `json:"retry_backoff_seconds"`
		Version             int     `json:"version"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	next := current.NextRunAt
	zone := current.Timezone
	cronExpr := current.CronExpr
	if req.Timezone != nil {
		zone = strings.TrimSpace(*req.Timezone)
		if zone == "" {
			zone = "UTC"
		}
		if _, err := time.LoadLocation(zone); err != nil {
			writeError(w, 400, "invalid timezone")
			return
		}
	}
	if req.CronExpr != nil {
		cronExpr = strings.TrimSpace(*req.CronExpr)
	}
	if cronExpr != current.CronExpr || zone != current.Timezone {
		next = nil
	}
	if cronExpr != "" && (cronExpr != current.CronExpr || zone != current.Timezone) {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sc, err := parser.Parse(cronExpr)
		if err != nil {
			writeError(w, 400, "invalid cron expression")
			return
		}
		t := sc.Next(time.Now().In(mustLocation(zone))).UTC()
		next = &t
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
		if current.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
	}
	if req.Prompt != nil {
		current.Prompt = strings.TrimSpace(*req.Prompt)
		if current.Prompt == "" || len(current.Prompt) > maxSchedulePromptBytes {
			writeError(w, http.StatusBadRequest, "prompt is invalid")
			return
		}
	}
	current.CronExpr = cronExpr
	current.Timezone = zone
	if req.ResultMode != nil {
		mode := strings.TrimSpace(*req.ResultMode)
		if mode != "new_session" && mode != "existing_session" {
			writeError(w, http.StatusBadRequest, "invalid result_mode")
			return
		}
		current.ResultMode = mode
	}
	if req.TargetSessionID != nil {
		current.TargetSessionID = strings.TrimSpace(*req.TargetSessionID)
	}
	if current.ResultMode == "existing_session" {
		if current.TargetSessionID == "" || !s.scheduleTargetAllowed(r.Context(), current.UserID, current.TargetSessionID) {
			writeError(w, http.StatusBadRequest, "target session not found")
			return
		}
	} else {
		current.TargetSessionID = ""
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.Archived != nil {
		current.Archived = *req.Archived
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds <= 0 {
			writeError(w, 400, "invalid timeout")
			return
		}
		current.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.MaxRetries != nil {
		if *req.MaxRetries < 0 || *req.MaxRetries > 10 {
			writeError(w, 400, "invalid max_retries")
			return
		}
		current.MaxRetries = *req.MaxRetries
	}
	if req.RetryBackoffSeconds != nil {
		if *req.RetryBackoffSeconds < 0 {
			writeError(w, 400, "invalid retry_backoff_seconds")
			return
		}
		current.RetryBackoffSeconds = *req.RetryBackoffSeconds
	}
	current.NextRunAt = next
	if req.Version > 0 {
		current.Version = req.Version
	}
	if err := s.store.UpdateSchedule(r.Context(), current, current.Version); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	updated, err := s.store.GetSchedule(r.Context(), current.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) scheduleTargetAllowed(ctx context.Context, userID, sessionID string) bool {
	session, err := s.store.GetSession(ctx, sessionID)
	return err == nil && session.UserID == userID && session.Mode == "agent" && session.Status != string(sessionstate.Destroyed)
}

func (s *Server) manualScheduleRun(w http.ResponseWriter, r *http.Request, sched store.AgentSchedule) {
	now := time.Now().UTC()
	run := store.ScheduleRun{ID: newID("srun"), ScheduleID: sched.ID, UserID: sched.UserID, ScheduledAt: now, Status: "queued", Attempt: 0, CreatedAt: now}
	if err := s.store.CreateScheduleRun(r.Context(), run); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	select {
	case s.schedulerWake <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusAccepted, run)
}
