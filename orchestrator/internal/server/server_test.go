package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/titler"
)

func TestCreateSessionRejectsLegacyAgentInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg: config.Config{

			MaxSessions:  10,
			DefaultAgent: "pi",
		},
		store: st,

		hub: events.NewHub(),
		log: slog.Default(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"agent":"opencode"}`))
	rec := httptest.NewRecorder()

	srv.createSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "agent input is no longer supported") {
		t.Fatalf("expected legacy agent error, got %s", rec.Body.String())
	}
}

func TestCreateSessionModeMapping(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMode   string
		wantDriver string
		wantError  string
	}{
		{name: "omitted body", wantStatus: http.StatusCreated, wantMode: "agent", wantDriver: "pi"},
		{name: "empty object", body: `{}`, wantStatus: http.StatusCreated, wantMode: "agent", wantDriver: "pi"},
		{name: "agent mode", body: `{"mode":"agent"}`, wantStatus: http.StatusCreated, wantMode: "agent", wantDriver: "pi"},
		{name: "shell mode", body: `{"mode":"shell"}`, wantStatus: http.StatusCreated, wantMode: "shell", wantDriver: "sh"},
		{name: "unknown mode", body: `{"mode":"pi"}`, wantStatus: http.StatusBadRequest, wantError: "unsupported mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
				cfg: config.Config{

					MaxSessions:  10,
					DefaultAgent: "pi",
				},
				store: st,

				hub: events.NewHub(),
				log: slog.Default(),
			}
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
			rec := httptest.NewRecorder()
			srv.createSession(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusCreated {
				if !strings.Contains(rec.Body.String(), tc.wantError) {
					t.Fatalf("expected error %q, got %s", tc.wantError, rec.Body.String())
				}
				var count int
				if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
					t.Fatalf("count sessions: %v", err)
				}
				if count != 0 {
					t.Fatalf("failed create should not persist sessions, got %d", count)
				}
				return
			}
			var created struct {
				ID        string `json:"id"`
				Mode      string `json:"mode"`
				ModeLabel string `json:"mode_label"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if created.Mode != tc.wantMode || created.ModeLabel != modeLabel(tc.wantMode) {
				t.Fatalf("public mode=%s label=%s want %s/%s", created.Mode, created.ModeLabel, tc.wantMode, modeLabel(tc.wantMode))
			}
			var driverID, storedMode string
			if err := st.DBForTest().QueryRowContext(ctx, `SELECT driver_id, mode FROM sessions WHERE id = ?`, created.ID).Scan(&driverID, &storedMode); err != nil {
				t.Fatalf("read stored session selector: %v", err)
			}
			if driverID != tc.wantDriver || storedMode != tc.wantMode {
				t.Fatalf("stored driver/mode=%s/%s want %s/%s", driverID, storedMode, tc.wantDriver, tc.wantMode)
			}
		})
	}
}

func TestDeploymentCapabilitiesAreProductSafe(t *testing.T) {
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg: config.Config{DefaultAgent: "pi"},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/deployment-capabilities", nil)
	rec := httptest.NewRecorder()
	srv.deploymentCapabilities(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"driver_id", "provider_id", "local_runsc", "host_path", "agent_manifest_digest"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("capability response exposed %q: %s", forbidden, body)
		}
	}
	var payload struct {
		SchemaVersion int    `json:"schema_version"`
		DefaultMode   string `json:"default_mode"`
		SessionModes  []struct {
			Mode           string  `json:"mode"`
			Label          string  `json:"label"`
			Visible        bool    `json:"visible"`
			CreateEnabled  bool    `json:"create_enabled"`
			DisabledReason *string `json:"disabled_reason"`
		} `json:"session_modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.SchemaVersion != 1 || payload.DefaultMode != "agent" || len(payload.SessionModes) != 2 {
		t.Fatalf("unexpected capabilities: %+v", payload)
	}
	for _, mode := range payload.SessionModes {
		if mode.Mode != "agent" && mode.Mode != "shell" {
			t.Fatalf("capabilities exposed non-product mode: %+v", mode)
		}
		if !mode.CreateEnabled || mode.DisabledReason != nil {
			t.Fatalf("expected test deployment mode enabled, got %+v", mode)
		}
	}
}

func TestAgentsCatalogIsOperatorOnly(t *testing.T) {
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true}, cfg: config.Config{CookieName: "sid"}}
	productReq := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	productRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(productRec, productReq)
	if productRec.Code != http.StatusNotFound {
		t.Fatalf("product /api/agents status=%d body=%s", productRec.Code, productRec.Body.String())
	}

	operatorReq := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	operatorRec := httptest.NewRecorder()
	srv.OperatorRoutes().ServeHTTP(operatorRec, operatorReq)
	if operatorRec.Code != http.StatusOK {
		t.Fatalf("operator /api/agents status=%d body=%s", operatorRec.Code, operatorRec.Body.String())
	}
	var payload struct {
		Drivers []struct {
			DriverID string `json:"driver_id"`
		} `json:"drivers"`
	}
	if err := json.Unmarshal(operatorRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode operator catalog: %v", err)
	}
	if len(payload.Drivers) != 2 ||
		payload.Drivers[0].DriverID != "pi" ||
		payload.Drivers[1].DriverID != "sh" {
		t.Fatalf("unexpected operator catalog: %+v", payload.Drivers)
	}
}

func TestCreateSessionUsesPublicSessionDTO(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hub := events.NewHub()
	eventsCh, cancelEvents := hub.Subscribe("")
	defer cancelEvents()
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg: config.Config{

			MaxSessions:  10,
			DefaultAgent: "pi",
		},
		store: st,

		hub: hub,
		log: slog.Default(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body %s", rec.Code, rec.Body.String())
	}
	assertPublicSessionJSONOmitsHostFields(t, rec.Body.Bytes())
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created["id"] == "" || created["mode"] != "agent" || created["mode_label"] != "Agent" {
		t.Fatalf("unexpected create response: %v", created)
	}

	select {
	case event := <-eventsCh:
		if event.Type != "session.created" {
			t.Fatalf("event type=%s want session.created", event.Type)
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatalf("marshal event payload: %v", err)
		}
		assertPublicSessionJSONOmitsHostFields(t, payload)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session.created event")
	}
}

func TestSessionReadResponsesUsePublicSessionDTO(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := createServerTestSession(t, ctx, st, dir, "sess_public", string(sessionstate.RunningIdle), now, nil)
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		store: st,
		hub:   events.NewHub(),
		log:   slog.Default(),
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session.ID, nil)
	getRec := httptest.NewRecorder()
	srv.getSession(getRec, getReq, session.ID)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d body %s", getRec.Code, getRec.Body.String())
	}
	assertPublicSessionJSONOmitsHostFields(t, getRec.Body.Bytes())

	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	listRec := httptest.NewRecorder()
	srv.listSessions(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d body %s", listRec.Code, listRec.Body.String())
	}
	assertPublicSessionJSONOmitsHostFields(t, listRec.Body.Bytes())
	assertContains(t, listRec.Body.String(), `"id":"sess_public"`)
}

func TestPublicEventSanitizerStripsRuntimePrivateFields(t *testing.T) {
	event := publicEvent(events.Event{
		EventID:      12,
		Type:         "session.checkpoint_retired",
		SessionID:    "sess_public_event",
		GenerationID: "gen_private",
		Payload: map[string]any{
			"session_status":           "running_idle",
			"session_updated_at":       "2026-05-26T01:02:00Z",
			"session_last_activity_at": "2026-05-26T00:30:00Z",
			"generation_id":            "gen_private",
			"active_generation_id":     "gen_private",
			"driver_id":                "pi",
			"agent":                    "pi",
			"restore_id":               "restore_private",
			"host_path":                filepath.Join(t.TempDir(), "private"),
			"driver_state": map[string]any{
				"state_digest": "sha256:private",
			},
			"data_volumes": map[string]any{
				"workspace": map[string]any{"host_path": "/host/workspace"},
			},
		},
	})
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal public event: %v", err)
	}
	body := string(data)
	for _, forbidden := range []string{
		`"generation_id"`,
		`"active_generation_id"`,
		`"driver_id"`,
		`"agent"`,
		`"restore_id"`,
		`"host_path"`,
		`"driver_state"`,
		`"data_volumes"`,
		"gen_private",
		"restore_private",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public event exposed %q: %s", forbidden, body)
		}
	}
	assertContains(t, body, `"event_id":12`)
	assertContains(t, body, `"session_id":"sess_public_event"`)
	assertContains(t, body, `"session_status":"running_idle"`)
	assertContains(t, body, `"session_updated_at":"2026-05-26T01:02:00Z"`)
}

func TestRenameSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_rename", string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg:   testServerConfig(dir),
		store: st,
		hub:   events.NewHub(),
		log:   slog.Default(),
	}

	rename := func(user, sessionID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sessionID, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, user))
		rec := httptest.NewRecorder()
		srv.renameSession(rec, req, sessionID)
		return rec
	}

	// Owner can rename; title persists and is marked custom.
	if rec := rename(labUserID, session.ID, `{"title":"  My Session  "}`); rec.Code != http.StatusOK {
		t.Fatalf("owner rename: expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "My Session" || !got.TitleCustom {
		t.Fatalf("after rename title = %q custom = %v", got.Title, got.TitleCustom)
	}

	// Empty / whitespace-only titles are rejected.
	if rec := rename(labUserID, session.ID, `{"title":"   "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty title: expected 400, got %d", rec.Code)
	}

	// Over-length titles are rejected.
	long := `{"title":"` + strings.Repeat("x", maxSessionTitleRunes+1) + `"}`
	if rec := rename(labUserID, session.ID, long); rec.Code != http.StatusBadRequest {
		t.Fatalf("long title: expected 400, got %d", rec.Code)
	}

	// A different user cannot rename (indistinguishable 404) and must not mutate.
	if rec := rename("someone-else", session.ID, `{"title":"hijack"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other-user rename: expected 404, got %d body %s", rec.Code, rec.Body.String())
	}
	got, _ = st.GetSession(ctx, session.ID)
	if got.Title != "My Session" {
		t.Fatalf("other-user rename mutated title to %q", got.Title)
	}
}

func TestGenerateLLMTitle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_llm", string(sessionstate.Created), time.Now().UTC(), nil)

	// Mock OpenAI-compatible endpoint returning a title.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Refactor Auth Module"}}]}`)
	}))
	defer mock.Close()

	hub := events.NewHub()
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg:   testServerConfig(dir),
		store: st,
		hub:   hub,
		log:   slog.Default(),
		titler: titler.New(titler.Config{
			BaseURL: mock.URL,
			APIKey:  "test-key",
			Model:   "glm-5.2",
		}),
	}

	srv.generateLLMTitle(ctx, session.ID, "please help me refactor the auth module")
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "Refactor Auth Module" || got.TitleCustom {
		t.Fatalf("llm title = %q custom = %v, want \"Refactor Auth Module\"/false", got.Title, got.TitleCustom)
	}

	// A user-authored title must not be overwritten by a later LLM result.
	if err := st.UpdateSessionTitle(ctx, session.ID, "My Title", true); err != nil {
		t.Fatalf("manual rename: %v", err)
	}
	srv.generateLLMTitle(ctx, session.ID, "please help me refactor the auth module")
	got, _ = st.GetSession(ctx, session.ID)
	if got.Title != "My Title" || !got.TitleCustom {
		t.Fatalf("llm clobbered custom title: %q custom = %v", got.Title, got.TitleCustom)
	}
}

func TestStartLLMTitleGenerationDisabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_llm_off", string(sessionstate.Created), time.Now().UTC(), nil)

	// No titler configured -> feature is a no-op, must not panic or change title.
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg:    testServerConfig(dir),
		store:  st,
		hub:    events.NewHub(),
		log:    slog.Default(),
		titler: titler.New(titler.Config{}),
	}
	srv.startLLMTitleGeneration(session.ID, "hello world")
	got, _ := st.GetSession(ctx, session.ID)
	if got.Title != "" {
		t.Fatalf("disabled titler mutated title to %q", got.Title)
	}
}

func TestPlaceholderTitleFromContent(t *testing.T) {
	long := strings.Repeat("a", 60)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "   ", ""},
		{"collapse whitespace", "  hello   world \n foo ", "hello world foo"},
		{"short kept", "short one", "short one"},
		{"truncated", long, strings.Repeat("a", placeholderTitleRunes) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := placeholderTitleFromContent(tt.in); got != tt.want {
				t.Fatalf("placeholderTitleFromContent(%q) = %q want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEventsStreamReplaysDurableEventsAfterLastEventID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	createServerTestSession(t, ctx, st, dir, "sess_a", string(sessionstate.RunningActive), now, nil)
	createServerTestSession(t, ctx, st, dir, "sess_b", string(sessionstate.RunningActive), now, nil)

	firstID, err := st.AppendEvent(ctx, store.AppendEventParams{
		SessionID: "sess_a",
		Type:      bridge.TypeAckTurnStarted,
		Payload:   map[string]string{"phase": "first"},
		Now:       now,
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	secondID, err := st.AppendEvent(ctx, store.AppendEventParams{
		SessionID: "sess_b",
		Type:      bridge.TypeEmitOutput,
		Payload:   map[string]string{"line": "second"},
		Now:       now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	thirdID, err := st.AppendEvent(ctx, store.AppendEventParams{
		SessionID: "sess_a",
		Type:      bridge.TypeAckTurnCompleted,
		Payload:   map[string]string{"status": "completed"},
		Now:       now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("append third event: %v", err)
	}

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true}, store: st, hub: events.NewHub(), log: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/api/events/stream?last_event_id="+strconv.FormatInt(firstID, 10), nil)
	lastEventID, ok, err := parseLastEventID(req)
	if err != nil || !ok || lastEventID != firstID {
		t.Fatalf("parse last_event_id: id=%d ok=%v err=%v", lastEventID, ok, err)
	}
	rec := httptest.NewRecorder()
	replayedThrough, err := srv.writeSSEReplay(req.Context(), rec, rec, "", lastEventID, labUserID)
	if err != nil {
		t.Fatalf("write replay: %v", err)
	}
	if replayedThrough != thirdID {
		t.Fatalf("replayed through=%d want %d", replayedThrough, thirdID)
	}
	body := rec.Body.String()
	if strings.Contains(body, "id: "+strconv.FormatInt(firstID, 10)+"\n") {
		t.Fatalf("replay included already-seen event: %s", body)
	}
	assertContains(t, body, "id: "+strconv.FormatInt(secondID, 10)+"\n")
	assertContains(t, body, "event: "+bridge.TypeEmitOutput+"\n")
	assertContains(t, body, `"event_id":`+strconv.FormatInt(secondID, 10))
	assertContains(t, body, `"session_id":"sess_b"`)
	assertContains(t, body, `"payload":{"line":"second"}`)
	assertContains(t, body, "id: "+strconv.FormatInt(thirdID, 10)+"\n")
	assertContains(t, body, "event: "+bridge.TypeAckTurnCompleted+"\n")
	if strings.Index(body, "id: "+strconv.FormatInt(secondID, 10)+"\n") >
		strings.Index(body, "id: "+strconv.FormatInt(thirdID, 10)+"\n") {
		t.Fatalf("replayed events out of order: %s", body)
	}

	filtered := httptest.NewRecorder()
	replayedThrough, err = srv.writeSSEReplay(req.Context(), filtered, filtered, "sess_a", firstID, labUserID)
	if err != nil {
		t.Fatalf("write filtered replay: %v", err)
	}
	if replayedThrough != thirdID {
		t.Fatalf("filtered replayed through=%d want %d", replayedThrough, thirdID)
	}
	filteredBody := filtered.Body.String()
	if strings.Contains(filteredBody, "id: "+strconv.FormatInt(secondID, 10)+"\n") {
		t.Fatalf("filtered replay included another session: %s", filteredBody)
	}
	assertContains(t, filteredBody, "id: "+strconv.FormatInt(thirdID, 10)+"\n")

	headerReq := httptest.NewRequest(http.MethodGet, "/api/events/stream?last_event_id=1", nil)
	headerReq.Header.Set("Last-Event-ID", strconv.FormatInt(thirdID, 10))
	lastEventID, ok, err = parseLastEventID(headerReq)
	if err != nil || !ok || lastEventID != thirdID {
		t.Fatalf("header Last-Event-ID should win: id=%d ok=%v err=%v", lastEventID, ok, err)
	}

	deleted, err := st.PruneEvents(ctx, store.PruneEventsParams{
		RetentionRows: 2,
		Now:           now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("prune replay events: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want 1", deleted)
	}

	gap := httptest.NewRecorder()
	replayedThrough, err = srv.writeSSEReplay(req.Context(), gap, gap, "", 0, labUserID)
	if err != nil {
		t.Fatalf("write gap replay: %v", err)
	}
	if replayedThrough != thirdID {
		t.Fatalf("gap replayed through=%d want %d", replayedThrough, thirdID)
	}
	gapBody := gap.Body.String()
	assertContains(t, gapBody, "id: "+strconv.FormatInt(secondID-1, 10)+"\n")
	assertContains(t, gapBody, "event: replay_gap\n")
	assertContains(t, gapBody, `"requested_last_event_id":0`)
	assertContains(t, gapBody, `"oldest_available":`+strconv.FormatInt(secondID, 10))
	assertContains(t, gapBody, `"session_id_filter":null`)
	assertContains(t, gapBody, `"reason":"retention_window_exceeded"`)
	assertContains(t, gapBody, "id: "+strconv.FormatInt(secondID, 10)+"\n")
	assertContains(t, gapBody, "id: "+strconv.FormatInt(thirdID, 10)+"\n")
	if strings.Contains(gapBody, `"payload":{"phase":"first"}`) {
		t.Fatalf("gap replay included pruned event: %s", gapBody)
	}

	filteredGap := httptest.NewRecorder()
	replayedThrough, err = srv.writeSSEReplay(req.Context(), filteredGap, filteredGap, "sess_a", 0, labUserID)
	if err != nil {
		t.Fatalf("write filtered gap replay: %v", err)
	}
	if replayedThrough != thirdID {
		t.Fatalf("filtered gap replayed through=%d want %d", replayedThrough, thirdID)
	}
	filteredGapBody := filteredGap.Body.String()
	assertContains(t, filteredGapBody, "id: "+strconv.FormatInt(thirdID-1, 10)+"\n")
	assertContains(t, filteredGapBody, "event: replay_gap\n")
	assertContains(t, filteredGapBody, `"oldest_available":`+strconv.FormatInt(thirdID, 10))
	assertContains(t, filteredGapBody, `"session_id_filter":"sess_a"`)
	assertContains(t, filteredGapBody, "id: "+strconv.FormatInt(thirdID, 10)+"\n")
	if strings.Contains(filteredGapBody, `"payload":{"line":"second"}`) {
		t.Fatalf("filtered gap replay included another session: %s", filteredGapBody)
	}
}

func openServerOwnedStore(t *testing.T, ctx context.Context, dir string) (*store.Store, *store.OwnerLock) {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	owner, err := store.AcquireOwnerLock(filepath.Join(dir, "run"))
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	return st, owner
}

func createServerTestSession(t *testing.T, ctx context.Context, st *store.Store, dir, id, status string, now time.Time, expiresAt *time.Time) store.Session {
	t.Helper()
	session := store.Session{
		ID:        id,
		UserID:    labUserID,
		Status:    status,
		Agent:     "pi",
		Workspace: filepath.Join(dir, "sessions", id),

		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := os.MkdirAll(session.Workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session
}

func assertPublicSessionJSONOmitsHostFields(t *testing.T, payload []byte) {
	t.Helper()
	body := string(payload)
	for _, field := range []string{
		`"workspace"`,
		`"agent_home_path"`,
		`"agent":`,
		`"active_generation_id":`,
		`"restore_id"`,
		`"checkpoint_path"`,
	} {
		if strings.Contains(body, field) {
			t.Fatalf("public session payload exposed host-only field %s: %s", field, body)
		}
	}
}

func TestShellOnlyDeploymentAdvertisesCreatableDefault(t *testing.T) {
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Shell: true}}
	response := httptest.NewRecorder()
	srv.deploymentCapabilities(response, httptest.NewRequest(http.MethodGet, "/api/deployment-capabilities", nil))
	var capabilities struct {
		Default string `json:"default_mode"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil || capabilities.Default != "shell" {
		t.Fatalf("unavailable default mode: %s %v", response.Body, err)
	}
}
