//go:build khoraintegration

package retainedapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/store"
)

// Uses the same HTTP admission and maintenance workers as the daemon, backed by
// the real Pi process and separately authenticated model gateway.
func verifyScheduledPiJourney(t *testing.T, ctx context.Context, app *server.Server, st *store.Store, session string) {
	t.Helper()
	host := httptest.NewServer(app.Routes())
	defer host.Close()
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, host.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s (%v)", method, path, response.StatusCode, want, raw, err)
		}
		return raw
	}
	var schedule store.AgentSchedule
	raw := request("POST", "/api/schedules", `{"name":"Real Pi schedule","prompt":"scheduled completion","timeout_seconds":30,"result_mode":"existing_session","target_session_id":"`+session+`"}`, 201)
	if err := json.Unmarshal(raw, &schedule); err != nil || schedule.ID == "" {
		t.Fatalf("schedule: %s %v", raw, err)
	}
	// A schedule belonging to another user is never readable or runnable, even
	// when its target happens to be a session owned by the current caller.
	foreign := schedule
	foreign.ID, foreign.UserID = "sched_foreign", "another-user"
	if err := st.CreateSchedule(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/schedules/"+foreign.ID, "", 404)
	request("POST", "/api/schedules/"+foreign.ID+"/run", "", 404)
	foreignTarget, err := st.GetSession(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	foreignTarget.ID, foreignTarget.UserID, foreignTarget.ActiveGenerationID = "sess_foreign", "another-user", ""
	if err := st.CreateSession(ctx, foreignTarget); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/schedules", `{"name":"Forbidden target","prompt":"forbidden","result_mode":"existing_session","target_session_id":"sess_foreign"}`, 400)
	for iteration := 0; iteration < 2; iteration++ {
		// Queue before starting maintenance. Restarting the worker must rediscover
		// durable work without depending on the transient wake notification.
		var queued store.ScheduleRun
		raw = request("POST", "/api/schedules/"+schedule.ID+"/run", "", 202)
		if err := json.Unmarshal(raw, &queued); err != nil || queued.ID == "" {
			t.Fatalf("run: %s %v", raw, err)
		}
		maintenance, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- app.RunRetainedMaintenance(maintenance, 100*time.Millisecond, 5*time.Second) }()
		func() {
			defer func() {
				stop()
				if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			}()
			for {
				var listing struct {
					Runs []store.ScheduleRun `json:"runs"`
				}
				raw = request("GET", "/api/schedules/"+schedule.ID+"/runs", "", 200)
				if err := json.Unmarshal(raw, &listing); err != nil {
					t.Fatal(err)
				}
				for _, run := range listing.Runs {
					if run.ID != queued.ID || run.Status == "queued" || run.Status == "running" {
						continue
					}
					if run.Status != "succeeded" || run.SessionID != session || run.TurnID == nil || !strings.Contains(run.OutputSummary, "gateway-carried-ok") {
						t.Fatalf("scheduled result: %+v", run)
					}
					return
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(250 * time.Millisecond):
				}
			}
		}()
	}
	request("DELETE", "/api/schedules/"+schedule.ID, "", 200)
	t.Log("real scheduled Pi turns completed through HTTP; worker restart and schedule/target ownership verified")
}
