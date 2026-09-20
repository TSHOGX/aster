package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func TestScheduleAPIUpdatesRoutingAndClearsCron(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true}, store: st}
	withUser := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), userContextKey{}, "alice"))
	}

	createdSession := store.Session{ID: "sess_target", UserID: "alice", Status: string(sessionstate.Created), Agent: "pi", Mode: "agent", Workspace: filepath.Join(t.TempDir(), "workspace"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.CreateSession(ctx, createdSession); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.createSchedule(rec, withUser(httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(`{"name":"Context run","prompt":"inspect files","cron_expr":"0 8 * * *","timezone":"UTC","result_mode":"existing_session","target_session_id":"sess_target"}`))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created store.AgentSchedule
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ResultMode != "existing_session" || created.TargetSessionID != "sess_target" {
		t.Fatalf("created routing fields = mode=%q target=%q", created.ResultMode, created.TargetSessionID)
	}

	body := `{"name":"Manual run","prompt":"updated","cron_expr":"","timezone":"UTC","result_mode":"new_session","target_session_id":"","version":1}`
	rec = httptest.NewRecorder()
	srv.scheduleRoute(rec, withUser(httptest.NewRequest(http.MethodPatch, "/api/schedules/"+created.ID, strings.NewReader(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := st.GetSchedule(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CronExpr != "" || updated.NextRunAt != nil || updated.ResultMode != "new_session" || updated.TargetSessionID != "" {
		t.Fatalf("updated schedule = %+v", updated)
	}
}

func TestScheduleAPIRejectsArchivedManualRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true}, store: st}
	now := time.Now().UTC()
	sc := store.AgentSchedule{ID: "sched_archived_api", UserID: "alice", Name: "Archived", Prompt: "run", Enabled: true, Mode: "agent", Archived: true, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/"+sc.ID+"/run", nil).WithContext(context.WithValue(context.Background(), userContextKey{}, "alice"))
	rec := httptest.NewRecorder()
	srv.scheduleRoute(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "archived") {
		t.Fatalf("archived run status=%d body=%s", rec.Code, rec.Body.String())
	}
}
