package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/artifacts"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/titler"
	localworkspace "harness-platform/orchestrator/internal/workspace/local"
)

func TestCreateSessionRejectsLegacyAgentInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := &Server{
		cfg: config.Config{
			SessionsRoot:     dir,
			SessionRetention: time.Hour,
			MaxSessions:      10,
			DefaultAgent:     "pi",
		},
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
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
			srv := &Server{
				cfg: config.Config{
					SessionsRoot:     dir,
					SessionRetention: time.Hour,
					MaxSessions:      10,
					DefaultAgent:     "pi",
				},
				store:   st,
				runtime: runtime.New(runtime.Config{}),
				watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
				hub:     events.NewHub(),
				log:     slog.Default(),
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

func TestValidateDriverStateForRuntimeLaunchPiHostState(t *testing.T) {
	agentHome := t.TempDir()
	volumes := sessionRuntimeDataVolumes{
		DriverHome: store.SessionDriverHomeVolume{HostPath: agentHome},
	}
	uninitialized := []byte(`{"driver_id":"pi","schema_version":1,"session_dir":"/agent-home/.pi/agent/sessions","state_kind":"pi_uninitialized"}`)
	if err := validateDriverStateForRuntimeLaunch(store.RuntimeGenerationDetails{
		Agent:              "pi",
		DriverStatePayload: uninitialized,
	}, volumes); err != nil {
		t.Fatalf("pi uninitialized launch state rejected: %v", err)
	}
	if err := validateDriverStateForRuntimeLaunch(store.RuntimeGenerationDetails{Agent: "pi"}, volumes); err == nil || !strings.Contains(err.Error(), "requires driver state payload") {
		t.Fatalf("expected missing pi driver state rejection, got %v", err)
	}

	sessionPayload := []byte(fmt.Sprintf(
		`{"driver_id":"pi","last_completed_turn_id":"9","schema_version":1,"selected_session_file":%q,"selected_session_id":"pi-session-1","selected_session_relpath":"session-1.jsonl","session_dir":%q,"state_kind":"pi_session"}`,
		agents.PiSessionDir+"/session-1.jsonl",
		agents.PiSessionDir,
	))
	if err := validateDriverStateForRuntimeLaunch(store.RuntimeGenerationDetails{
		Agent:              "pi",
		DriverStatePayload: sessionPayload,
	}, volumes); err == nil || !strings.Contains(err.Error(), "host file missing") {
		t.Fatalf("expected missing pi session file rejection, got %v", err)
	}

	sessionRoot := filepath.Join(agentHome, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("create pi session root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "session-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write pi session file: %v", err)
	}
	if err := validateDriverStateForRuntimeLaunch(store.RuntimeGenerationDetails{
		Agent:              "pi",
		DriverStatePayload: sessionPayload,
	}, volumes); err != nil {
		t.Fatalf("pi session launch state rejected: %v", err)
	}
	if err := validateDriverStateForRuntimeLaunch(store.RuntimeGenerationDetails{Agent: "sh"}, sessionRuntimeDataVolumes{}); err != nil {
		t.Fatalf("non-pi runtime launch should not require driver state: %v", err)
	}
}

func TestCreateSessionAgentModeRejectsShellDefault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{
		cfg: config.Config{
			SessionsRoot:     dir,
			SessionRetention: time.Hour,
			MaxSessions:      10,
			DefaultAgent:     "sh",
		},
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent mode unavailable") {
		t.Fatalf("expected unavailable agent mode, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	var count int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed create should not persist sessions, got %d", count)
	}
}

func TestDeploymentCapabilitiesAreProductSafe(t *testing.T) {
	srv := &Server{
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

func TestDeploymentCapabilitiesUseImageManifestGate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rootfs := filepath.Join(dir, "rootfs")
	writeServerTestAgentImageManifest(t, rootfs, agents.Pi)
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{
		cfg: config.Config{
			SessionsRoot:         dir,
			SessionRetention:     time.Hour,
			MaxSessions:          10,
			DefaultAgent:         "pi",
			RootFSPath:           rootfs,
			RequireAgentManifest: true,
		},
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/deployment-capabilities", nil)
	rec := httptest.NewRecorder()
	srv.deploymentCapabilities(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var capabilities struct {
		SessionModes []struct {
			Mode           string  `json:"mode"`
			Visible        bool    `json:"visible"`
			CreateEnabled  bool    `json:"create_enabled"`
			DisabledReason *string `json:"disabled_reason"`
		} `json:"session_modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	var sawAgent, sawShell bool
	for _, mode := range capabilities.SessionModes {
		switch mode.Mode {
		case "agent":
			sawAgent = true
			if !mode.Visible || !mode.CreateEnabled || mode.DisabledReason != nil {
				t.Fatalf("agent should remain available without sh in image: %+v", mode)
			}
		case "shell":
			sawShell = true
			if mode.Visible || mode.CreateEnabled || mode.DisabledReason == nil || *mode.DisabledReason != "missing_from_image" {
				t.Fatalf("shell should be hidden when sh is absent from image: %+v", mode)
			}
		}
	}
	if !sawAgent || !sawShell {
		t.Fatalf("expected agent and shell modes: %+v", capabilities.SessionModes)
	}

	shellReq := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"shell"}`))
	shellRec := httptest.NewRecorder()
	srv.createSession(shellRec, shellReq)
	if shellRec.Code != http.StatusBadRequest || !strings.Contains(shellRec.Body.String(), "shell mode unavailable") {
		t.Fatalf("expected shell rejection before persistence, got status=%d body=%s", shellRec.Code, shellRec.Body.String())
	}
	var count int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("shell rejection should not persist a session, got %d", count)
	}

	agentReq := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	agentRec := httptest.NewRecorder()
	srv.createSession(agentRec, agentReq)
	if agentRec.Code != http.StatusCreated {
		t.Fatalf("expected agent session creation, got status=%d body=%s", agentRec.Code, agentRec.Body.String())
	}
}

func TestCreateSessionFailsClosedWhenRequiredManifestMissing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{
		cfg: config.Config{
			SessionsRoot:         dir,
			SessionRetention:     time.Hour,
			MaxSessions:          10,
			DefaultAgent:         "pi",
			RootFSPath:           filepath.Join(dir, "missing-rootfs"),
			RequireAgentManifest: true,
		},
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "agent mode unavailable") {
		t.Fatalf("expected fail-closed agent rejection, got status=%d body=%s", rec.Code, rec.Body.String())
	}
	var count int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("manifest rejection should not persist a session, got %d", count)
	}
}

func TestDriverManifestInputDigestsUseSourceConfigAndImageManifest(t *testing.T) {
	dir := t.TempDir()
	rootfs := filepath.Join(dir, "rootfs")
	writeServerTestAgentImageManifest(t, rootfs, agents.Pi, agents.Shell)
	cfg := config.Config{
		DefaultAgent:         "sh",
		RootFSPath:           rootfs,
		RequireAgentManifest: true,
	}
	srv := &Server{cfg: cfg}
	deployment, capabilityErr := srv.resolveModeDeployment("shell")
	if capabilityErr != nil {
		t.Fatalf("resolve shell deployment: %v", capabilityErr)
	}
	manifest, err := srv.loadAgentImageManifest()
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	digests := srv.driverManifestInputDigests(deployment)
	if digests.AgentManifestDigest != manifest.Digest {
		t.Fatalf("agent manifest digest = %s want %s", digests.AgentManifestDigest, manifest.Digest)
	}
	if want := runtimeConfigDigest(deployment.runtimeConfigPreimage("sh")); digests.RuntimeConfigDigest != want {
		t.Fatalf("runtime config digest = %s want %s", digests.RuntimeConfigDigest, want)
	}

	cfg.DefaultAgent = "pi"
	srv = &Server{cfg: cfg}
	deploymentWithPiDefault, capabilityErr := srv.resolveModeDeployment("shell")
	if capabilityErr != nil {
		t.Fatalf("resolve shell deployment with pi default: %v", capabilityErr)
	}
	changed := srv.driverManifestInputDigests(deploymentWithPiDefault)
	if changed.RuntimeConfigDigest == digests.RuntimeConfigDigest {
		t.Fatalf("shell runtime config digest should change when deployment default agent changes")
	}
	if changed.AgentManifestDigest != digests.AgentManifestDigest {
		t.Fatalf("agent manifest digest should not change when manifest is unchanged: %s vs %s", changed.AgentManifestDigest, digests.AgentManifestDigest)
	}
}

func TestAgentsCatalogIsOperatorOnly(t *testing.T) {
	srv := &Server{cfg: config.Config{CookieName: "sid"}, runtime: runtime.New(runtime.Config{})}
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

func TestCreateSessionSoftLimitUsesPoolExhaustedEnvelope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	cfg.MaxSessions = 1
	cfg.Harness.MaxSessions = 1

	// The ceiling counts sessions holding a live sandbox. A session that is
	// merely idle with its sandbox still live occupies a slot, so the next
	// create must be rejected.
	session := createServerTestSession(t, ctx, st, dir, "sess_existing", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, cfg.SessionsRoot, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error_class"] != "pool_exhausted" {
		t.Fatalf("expected pool_exhausted, got %v", body)
	}
}

func TestCreateSessionSoftLimitIgnoresCheckpointedSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	cfg.MaxSessions = 1
	cfg.Harness.MaxSessions = 1

	// A checkpointed session has released its sandbox compute, so it must not
	// count against the concurrent-sandbox ceiling: creating a new session is
	// allowed even though a checkpointed session already exists.
	session := createServerTestSession(t, ctx, st, dir, "sess_checkpointed", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)
	markServerGenerationCheckpointed(t, ctx, st, session.ID, allocation.GenerationID, time.Now().UTC())
	if err := st.UpdateSessionStatus(ctx, session.ID, string(sessionstate.Checkpointed), nil); err != nil {
		t.Fatalf("mark session checkpointed: %v", err)
	}

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, cfg.SessionsRoot, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201 (checkpointed session should not hold a slot), got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCloseSessionReleasesSoftLimitWithoutDeletingHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)

	cfg := testServerConfig(dir)
	cfg.SessionRetention = 0
	cfg.MaxSessions = 1
	cfg.Harness.MaxSessions = 1
	now := time.Now().UTC()
	oldSession := store.Session{
		ID:            "sess_retained",
		UserID:        labUserID,
		Status:        string(sessionstate.Created),
		Agent:         "pi",
		Workspace:     filepath.Join(cfg.SessionsRoot, "sess_retained"),
		AgentHomePath: filepath.Join(dir, "agent-homes", "sess_retained"),
		RestoreID:     "phase3-sess_retained",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := os.MkdirAll(oldSession.Workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.MkdirAll(oldSession.AgentHomePath, 0o755); err != nil {
		t.Fatalf("create agent home: %v", err)
	}
	if err := st.CreateSession(ctx, oldSession); err != nil {
		t.Fatalf("create retained session: %v", err)
	}
	if _, err := st.AddMessage(ctx, oldSession.ID, "user", "keep this"); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if err := st.UpsertArtifact(ctx, store.Artifact{
		SessionID: oldSession.ID,
		Path:      "report.txt",
		Size:      12,
		ModTime:   now,
	}); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	// Give the session a live sandbox so it occupies a slot against the ceiling.
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: oldSession.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, oldSession.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, oldSession.ID, allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: &recordingRuntime{},
		watcher: newServerTestWatcher(t, cfg.SessionsRoot, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected quota rejection before close, got %d body %s", rec.Code, rec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+oldSession.ID, nil)
	deleteRec := httptest.NewRecorder()
	srv.destroySession(deleteRec, deleteReq, oldSession.ID)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected close status 200, got %d body %s", deleteRec.Code, deleteRec.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	createRec := httptest.NewRecorder()
	srv.createSession(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected create after close, got %d body %s", createRec.Code, createRec.Body.String())
	}

	closed, err := st.GetSession(ctx, oldSession.ID)
	if err != nil {
		t.Fatalf("get closed session: %v", err)
	}
	if closed.Status != string(sessionstate.Destroyed) ||
		closed.Workspace != oldSession.Workspace ||
		closed.AgentHomePath != oldSession.AgentHomePath {
		t.Fatalf("closed session should preserve terminal state and paths: %+v", closed)
	}
	if _, err := os.Stat(oldSession.Workspace); err != nil {
		t.Fatalf("workspace should remain after close: %v", err)
	}
	if _, err := os.Stat(oldSession.AgentHomePath); err != nil {
		t.Fatalf("agent home should remain after close: %v", err)
	}
	messages, err := st.ListMessages(ctx, oldSession.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "keep this" {
		t.Fatalf("expected retained message, got %+v", messages)
	}
	artifacts, err := st.ListArtifacts(ctx, oldSession.ID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Path != "report.txt" {
		t.Fatalf("expected retained artifact, got %+v", artifacts)
	}
}

func TestCreateSessionUsesNullExpiryWhenSessionRetentionDisabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := &Server{
		cfg: config.Config{
			SessionsRoot:     dir,
			SessionRetention: 0,
			MaxSessions:      10,
			DefaultAgent:     "pi",
		},
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body %s", rec.Code, rec.Body.String())
	}
	var created store.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if created.ExpiresAt != nil {
		t.Fatalf("expected nil expires_at in response, got %v", created.ExpiresAt)
	}
	got, err := st.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("get created session: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Fatalf("expected nil stored expires_at, got %v", got.ExpiresAt)
	}
	changed, err := st.SweepExpiredSessions(ctx, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("sweep sessions: %v", err)
	}
	if changed != 0 {
		t.Fatalf("expected NULL expires_at session to be preserved, swept %d", changed)
	}
	got, err = st.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("get session after sweep: %v", err)
	}
	if got.Status != string(sessionstate.Created) {
		t.Fatalf("expected session to remain created, got %s", got.Status)
	}
}

func TestCreateSessionDefersWorkspaceProvisioning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := testServerConfig(dir)
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, cfg.SessionsRoot, st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"agent"}`))
	rec := httptest.NewRecorder()
	srv.createSession(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	session, err := st.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("get created session: %v", err)
	}
	if strings.HasPrefix(session.RestoreID, "phase3-") {
		t.Fatalf("new sessions must not receive phase3 restore ids: %+v", session)
	}
	if _, err := os.Stat(session.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session create should defer workspace provisioning, stat err=%v path=%s", err, session.Workspace)
	}
	var workspaceRows int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_workspaces WHERE session_id = ?`, session.ID).Scan(&workspaceRows); err != nil {
		t.Fatalf("count workspace evidence rows: %v", err)
	}
	if workspaceRows != 0 {
		t.Fatalf("session create should not synthesize workspace evidence rows, got %d", workspaceRows)
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
	srv := &Server{
		cfg: config.Config{
			SessionsRoot:     dir,
			SessionRetention: time.Hour,
			MaxSessions:      10,
			DefaultAgent:     "pi",
		},
		store:   st,
		runtime: runtime.New(runtime.Config{}),
		watcher: newServerTestWatcher(t, dir, st, events.NewHub()),
		hub:     hub,
		log:     slog.Default(),
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
	restoreMS := int64(123)
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE sessions
SET agent_home_path = ?,
    checkpoint_path = ?,
    restore_ms = ?
WHERE id = ?`, filepath.Join(dir, "agent-homes", session.ID), filepath.Join(dir, "checkpoints", session.ID), restoreMS, session.ID); err != nil {
		t.Fatalf("seed host-only fields: %v", err)
	}

	srv := &Server{
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
	assertContains(t, getRec.Body.String(), `"restore_ms":123`)

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

func TestMonitorIdleSessionsSkipsHostNetwork(t *testing.T) {
	srv := &Server{
		cfg: config.Config{
			RunscNetwork: "host",
		},
		log: slog.Default(),
	}

	if err := srv.MonitorIdleSessions(context.Background()); err != nil {
		t.Fatalf("expected idle monitor to exit cleanly in host mode, got %v", err)
	}
}

func TestMonitorIdleSessionsReEnablesCheckpointedSessionsWhenCheckpointDisabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := store.Session{
		ID:        "sess_checkpointed",
		UserID:    "lab",
		Status:    string(sessionstate.Checkpointed),
		Agent:     "pi",
		Workspace: filepath.Join(dir, "sessions", "sess_checkpointed"),
		RestoreID: "phase3-sess_checkpointed",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	srv := &Server{
		cfg: config.Config{
			RunscNetwork: "sandbox",
		},
		store: st,
		hub:   events.NewHub(),
		log:   slog.Default(),
	}
	if err := srv.MonitorIdleSessions(context.Background()); err != nil {
		t.Fatalf("monitor idle sessions: %v", err)
	}
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != string(sessionstate.Checkpointed) {
		t.Fatalf("disabled monitor should leave checkpointed session alone, got %s", got.Status)
	}
}

func TestMonitorIdleSessionsReleasesEligibleGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_auto_release", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	enableSessionAutoCheckpoint(t, ctx, st, session.ID)
	cfg := testServerConfig(dir)
	cfg.Harness.Checkpoint.AutoEnabled = true
	cfg.Harness.Checkpoint.IdleThreshold = config.Duration{Duration: time.Nanosecond}
	cfg.Harness.Checkpoint.MonitorInterval = config.Duration{Duration: time.Hour}
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)
	rt := &recordingRuntime{}
	runCtx, cancel := context.WithCancel(ctx)
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.MonitorIdleSessions(runCtx)
	}()
	waitForSessionStatus(t, ctx, st, session.ID, string(sessionstate.Checkpointed))
	// Reaching 'checkpointed' only means the DB transition landed;
	// releaseIdleGeneration tears the sandbox down *after* that, on the same
	// context. Cancelling here would race that teardown — the monitor would
	// abort mid-cleanup ("teardown deferred to reaper") and the destroy
	// assertion below would see nothing. Wait for the teardown the test is
	// actually asserting on before cancelling.
	waitForDestroyRequest(t, rt, allocation.GenerationID)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("monitor exit err=%v, want context canceled", err)
	}

	// Release tears the sandbox down (it never took a runsc checkpoint).
	if destroys := rt.destroyGenerationRequests(); len(destroys) != 1 || destroys[0].GenerationID != allocation.GenerationID {
		t.Fatalf("release should tear down the generation sandbox, got %+v", destroys)
	}

	var generationStatus, errorClass, networkState, resourceState, sessionCheckpointPath string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state, COALESCE(s.checkpoint_path, '')
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN sessions s ON s.id = g.session_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(
		&generationStatus, &errorClass, &networkState, &resourceState, &sessionCheckpointPath,
	); err != nil {
		t.Fatalf("query released generation: %v", err)
	}
	// Generation is failed (cold-start on wake), no checkpoint image, resources
	// destroyed so the slot is freed.
	if generationStatus != "failed" ||
		errorClass != store.ReleaseIdleGenerationErrorClass ||
		networkState != "destroyed" ||
		resourceState != "destroyed" ||
		sessionCheckpointPath != "" {
		t.Fatalf("unexpected released state: generation=%s err=%s network=%s resource=%s sessionCkpt=%q",
			generationStatus, errorClass, networkState, resourceState, sessionCheckpointPath)
	}
}

func TestMonitorIdleSessionsSkipsGenerationWithInflightTurn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_release_busy", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	enableSessionAutoCheckpoint(t, ctx, st, session.ID)
	cfg := testServerConfig(dir)
	cfg.Harness.Checkpoint.AutoEnabled = true
	cfg.Harness.Checkpoint.IdleThreshold = config.Duration{Duration: time.Nanosecond}
	cfg.Harness.Checkpoint.MonitorInterval = config.Duration{Duration: time.Hour}
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)
	// An in-flight turn must block release (the sandbox is still working).
	if _, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{
		SessionID: session.ID,
		Content:   "still working",
		Now:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}

	candidates, err := st.ListIdleReleaseCandidates(ctx, store.GenerationLeaseOwner(owner.UUID), time.Now().UTC(), time.Nanosecond)
	if err != nil {
		t.Fatalf("list idle release candidates: %v", err)
	}
	for _, c := range candidates {
		if c.GenerationID == allocation.GenerationID {
			t.Fatalf("generation with in-flight turn must not be a release candidate: %+v", candidates)
		}
	}
}

func testDriveCheckpoint(t *testing.T, ctx context.Context, srv *Server, st *store.Store, sessionID, generationID, owner string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.BeginGenerationCheckpoint(ctx, sessionID, generationID, owner, now); err != nil {
		t.Fatalf("begin checkpoint: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, sessionID, generationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	if err := st.CompleteGenerationCheckpoint(ctx, store.CompleteCheckpointParams{
		SessionID:                       sessionID,
		GenerationID:                    generationID,
		Owner:                           owner,
		CheckpointPath:                  details.CheckpointPath,
		RunscPlatform:                   details.RunscPlatform,
		RunscVersion:                    details.RunscVersion,
		RunscBinaryPath:                 details.RunscBinaryPath,
		RunscBinaryDigest:               details.RunscBinaryDigest,
		CheckpointBundleDigest:          details.BundleDigest,
		CheckpointRuntimeConfigDigest:   details.RuntimeConfigDigest,
		CheckpointControlManifestDigest: details.ProjectedControlManifestDigest,
		Now:                             time.Now().UTC(),
	}); err != nil {
		t.Fatalf("complete checkpoint: %v", err)
	}
	instance, err := st.GetRuntimeResourceInstance(ctx, generationID)
	if err != nil {
		t.Fatalf("get runtime resource instance: %v", err)
	}
	if err := st.ReserveRuntimeResourceCheckpoint(ctx, store.RuntimeResourceWorkerTransitionParams{
		GenerationID: generationID,
		WorkerID:     instance.WorkerID,
		HostID:       instance.HostID,
		Now:          time.Now().UTC(),
	}); err != nil {
		t.Fatalf("reserve runtime resource checkpoint: %v", err)
	}
}

func TestSendMessageAllocatesGenerationAndQueuesBridgeTurn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_turn", string(sessionstate.Created), time.Now().UTC(), nil)

	srv := &Server{
		cfg:     testServerConfig(dir),
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"hello"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	waitForSessionStatus(t, ctx, st, session.ID, string(sessionstate.RunningActive))

	var generations, networkRows, resourceRows, queuedTurns, userMessages int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_generations WHERE session_id = ?`, session.ID).Scan(&generations); err != nil {
		t.Fatalf("count generations: %v", err)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM network_profiles WHERE session_id = ? AND allocation_state = 'live'`, session.ID).Scan(&networkRows); err != nil {
		t.Fatalf("count network rows: %v", err)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM runtime_generation_resources r
JOIN runtime_generations g ON g.generation_id = r.generation_id
WHERE g.session_id = ? AND r.resource_state = 'live'`, session.ID).Scan(&resourceRows); err != nil {
		t.Fatalf("count resource rows: %v", err)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ? AND status = 'queued' AND generation_id IS NULL`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	// Zero, deliberately: enqueuing creates the turn, and the transcript entry is
	// written when the agent starts it. No bridge runs in this fixture, so the turn
	// stays queued and unasked.
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id = ? AND role = 'user' AND content = 'hello'`, session.ID).Scan(&userMessages); err != nil {
		t.Fatalf("count user messages: %v", err)
	}
	if generations != 1 || networkRows != 1 || resourceRows != 1 || queuedTurns != 1 || userMessages != 0 {
		t.Fatalf("unexpected bridge enqueue rows: generations=%d network=%d resources=%d queued_turns=%d user_messages=%d (want 1/1/1/1/0)", generations, networkRows, resourceRows, queuedTurns, userMessages)
	}
}

func TestSendMessageReusesActiveGenerationArtifacts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_reuse", string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{
		cfg:     testServerConfig(dir),
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	atomic.StoreInt64(&instantRuntimePrepareCalls, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"first"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected first status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	waitForSessionStatus(t, ctx, st, session.ID, string(sessionstate.RunningActive))
	if got := atomic.LoadInt64(&instantRuntimePrepareCalls); got != 1 {
		t.Fatalf("first turn prepare calls=%d want 1", got)
	}
	var generationID string
	var firstTurnID int64
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.generation_id, t.id
FROM runtime_generations g
JOIN turns t ON t.session_id = g.session_id
WHERE g.session_id = ?
  AND t.status = 'queued'
  AND t.content = 'first'`, session.ID).Scan(&generationID, &firstTurnID); err != nil {
		t.Fatalf("query first queued turn: %v", err)
	}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	if grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{
		SessionID:    session.ID,
		GenerationID: generationID,
		Owner:        leaseOwner,
		RequestID:    "req_first",
		LeaseTTL:     time.Minute,
		Now:          time.Now().UTC(),
	}); err != nil || !ok || grant.TurnID != firstTurnID {
		t.Fatalf("claim first turn: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	if _, err := st.AckTurnStarted(ctx, store.AckStartedParams{
		SessionID:       session.ID,
		GenerationID:    generationID,
		TurnID:          firstTurnID,
		Owner:           leaseOwner,
		SandboxSourceIP: serverSandboxSourceIPForGeneration(t, ctx, st, generationID),
		LeaseTTL:        time.Minute,
		Now:             time.Now().UTC(),
	}); err != nil {
		t.Fatalf("ack first turn started: %v", err)
	}
	if _, err := st.CompleteTurn(ctx, store.CompleteTurnParams{
		SessionID:      session.ID,
		GenerationID:   generationID,
		TurnID:         firstTurnID,
		Owner:          leaseOwner,
		TerminalStatus: "completed",
		Now:            time.Now().UTC(),
	}); err != nil {
		t.Fatalf("complete first turn: %v", err)
	}
	if err := st.UpdateSessionStatusAndActivity(ctx, session.ID, string(sessionstate.RunningIdle), nil, time.Now().UTC()); err != nil {
		t.Fatalf("mark session idle: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"second"}`))
	rec = httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected second status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	waitForSessionStatus(t, ctx, st, session.ID, string(sessionstate.RunningActive))
	if got := atomic.LoadInt64(&instantRuntimePrepareCalls); got != 1 {
		t.Fatalf("active generation should reuse prepared artifacts, prepare calls=%d", got)
	}
	var completedTurns, queuedTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ? AND status = 'completed'`, session.ID).Scan(&completedTurns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ? AND status = 'queued' AND content = 'second'`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count queued turns: %v", err)
	}
	if completedTurns != 1 || queuedTurns != 1 {
		t.Fatalf("unexpected turn statuses after reuse: completed=%d queued=%d", completedTurns, queuedTurns)
	}
}

func TestSendMessageColdFallbackAllocatesReplacementGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_send_fallback", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate old generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark old generation live: %v", err)
	}
	if err := st.FailGeneration(ctx, store.FailGenerationParams{
		SessionID:    session.ID,
		GenerationID: old.GenerationID,
		Owner:        old.Owner,
		ErrorClass:   "orchestrator_restart_reconnect_grace_expired",
		Reason:       "orchestrator_restart_reconnect_grace_expired",
		Now:          time.Now().UTC(),
	}); err != nil {
		t.Fatalf("fail old generation: %v", err)
	}

	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after fallback"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}

	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.ActiveGenerationID == "" || gotSession.ActiveGenerationID == old.GenerationID {
		t.Fatalf("active generation was not replaced: %q old=%q", gotSession.ActiveGenerationID, old.GenerationID)
	}
	if gotSession.Workspace != session.Workspace ||
		gotSession.AgentHomePath == "" {
		t.Fatalf("session identity not preserved: before=%+v after=%+v", session, gotSession)
	}
	var oldStatus, oldNetwork, oldResources, newStatus, newNetwork, newResources string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, old.GenerationID).Scan(&oldStatus, &oldNetwork, &oldResources); err != nil {
		t.Fatalf("query old generation: %v", err)
	}
	if oldStatus != "failed" || oldNetwork != "reclaimable" || oldResources != "reclaimable" {
		t.Fatalf("old generation not fenced/reclaimable: status=%s network=%s resources=%s", oldStatus, oldNetwork, oldResources)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, gotSession.ActiveGenerationID).Scan(&newStatus, &newNetwork, &newResources); err != nil {
		t.Fatalf("query replacement generation: %v", err)
	}
	if newStatus != "idle" || newNetwork != "live" || newResources != "live" {
		t.Fatalf("replacement generation not live idle: status=%s network=%s resources=%s", newStatus, newNetwork, newResources)
	}
	var queuedTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND status = 'queued'
  AND generation_id IS NULL
  AND content = 'after fallback'`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count queued turns: %v", err)
	}
	if queuedTurns != 1 {
		t.Fatalf("queued fallback turn count=%d want 1", queuedTurns)
	}
	prepareRequests, startRequests := rt.requests()
	if len(prepareRequests) != 1 || len(startRequests) != 1 {
		t.Fatalf("runtime calls prepare=%d start=%d", len(prepareRequests), len(startRequests))
	}
	if startRequests[0].GenerationID != gotSession.ActiveGenerationID ||
		startRequests[0].WaitForTurn {
		t.Fatalf("unexpected replacement start request: %+v", startRequests[0])
	}
}

func TestSendMessageRestoresCheckpointedGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_restore", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate checkpointed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark checkpointed generation live: %v", err)
	}
	recordServerRuntimeArtifacts(t, ctx, st, allocation.GenerationID, "restore_manifest_digest", "runsc restore-test")
	markServerGenerationCheckpointed(t, ctx, st, session.ID, allocation.GenerationID, time.Now().UTC())

	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after restore"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}

	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get restored session: %v", err)
	}
	if gotSession.ActiveGenerationID != allocation.GenerationID || gotSession.Status != string(sessionstate.RunningActive) {
		t.Fatalf("unexpected restored session: %+v allocation=%+v", gotSession, allocation)
	}
	var generationStatus, networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &networkState, &resourceState); err != nil {
		t.Fatalf("query restored generation: %v", err)
	}
	if generationStatus != "idle" || networkState != "live" || resourceState != "live" {
		t.Fatalf("restored generation not live idle: status=%s network=%s resources=%s", generationStatus, networkState, resourceState)
	}
	var queuedTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND status = 'queued'
  AND generation_id IS NULL
  AND content = 'after restore'`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count restored queued turns: %v", err)
	}
	if queuedTurns != 1 {
		t.Fatalf("queued restored turn count=%d want 1", queuedTurns)
	}
	prepareRequests, startRequests := rt.requests()
	if len(prepareRequests) != 0 || len(startRequests) != 1 {
		t.Fatalf("restore should skip prepare and start once: prepare=%d start=%d", len(prepareRequests), len(startRequests))
	}
	start := startRequests[0]
	if start.GenerationID != allocation.GenerationID ||
		!start.RestoreFromCheckpoint ||
		start.WaitForTurn ||
		start.PreparedArtifacts.ManifestDigest != "restore_manifest_digest" ||
		start.PreparedArtifacts.RunscVersion != "runsc restore-test" ||
		start.PreparedArtifacts.RunscBinaryPath != "/usr/local/bin/runsc-test" ||
		start.PreparedArtifacts.RunscBinaryDigest != "sha256:runsc-test" ||
		start.Generation.NetworkAllocationState != "recreating" {
		t.Fatalf("unexpected restore start request: %+v", start)
	}
}

func TestSendMessageFallsBackWhenCheckpointRestoreFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_restore_fallback", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate checkpointed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark checkpointed generation live: %v", err)
	}
	recordServerRuntimeArtifacts(t, ctx, st, old.GenerationID, "restore_manifest_digest", "runsc restore-test")
	markServerGenerationCheckpointed(t, ctx, st, session.ID, old.GenerationID, time.Now().UTC())
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    restore_ms = 123
WHERE id = ?`, filepath.Join(dir, "checkpoints", session.ID), session.ID); err != nil {
		t.Fatalf("seed checkpoint metadata: %v", err)
	}

	rt := &restoreFailoverRuntime{err: errors.New("checkpoint_runsc_version mismatch")}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after restore fallback"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get fallback session: %v", err)
	}
	if gotSession.ActiveGenerationID == "" || gotSession.ActiveGenerationID == old.GenerationID || gotSession.Status != string(sessionstate.RunningActive) {
		t.Fatalf("fallback did not replace active generation: %+v old=%s", gotSession, old.GenerationID)
	}
	if gotSession.CheckpointPath != "" || gotSession.RestoreMS != nil {
		t.Fatalf("fallback should clear checkpoint metadata: checkpoint=%q restore=%v", gotSession.CheckpointPath, gotSession.RestoreMS)
	}
	var oldStatus, oldNetwork, oldResources, oldErrorClass, newStatus, newNetwork, newResources string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state, COALESCE(g.error_class, '')
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, old.GenerationID).Scan(&oldStatus, &oldNetwork, &oldResources, &oldErrorClass); err != nil {
		t.Fatalf("query old generation: %v", err)
	}
	if oldStatus != "failed" || oldNetwork != "reclaimable" || oldResources != "reclaimable" || oldErrorClass != "runtime_failed" {
		t.Fatalf("old generation not fenced after restore failure: status=%s network=%s resources=%s class=%s", oldStatus, oldNetwork, oldResources, oldErrorClass)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, gotSession.ActiveGenerationID).Scan(&newStatus, &newNetwork, &newResources); err != nil {
		t.Fatalf("query fallback generation: %v", err)
	}
	if newStatus != "idle" || newNetwork != "live" || newResources != "live" {
		t.Fatalf("fallback generation not live idle: status=%s network=%s resources=%s", newStatus, newNetwork, newResources)
	}
	var queuedTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND status = 'queued'
  AND generation_id IS NULL
  AND content = 'after restore fallback'`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count fallback queued turns: %v", err)
	}
	if queuedTurns != 1 {
		t.Fatalf("queued fallback turn count=%d want 1", queuedTurns)
	}
	prepareRequests, startRequests := rt.requests()
	if len(prepareRequests) != 1 || len(startRequests) != 2 {
		t.Fatalf("runtime calls prepare=%d start=%d", len(prepareRequests), len(startRequests))
	}
	if !startRequests[0].RestoreFromCheckpoint || startRequests[0].GenerationID != old.GenerationID {
		t.Fatalf("first start was not restore: %+v", startRequests[0])
	}
	if startRequests[1].RestoreFromCheckpoint || startRequests[1].GenerationID != gotSession.ActiveGenerationID {
		t.Fatalf("second start was not cold fallback: %+v", startRequests[1])
	}
	var restoreRetiredEvents, runtimeEvents, terminalEvents int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT
  SUM(CASE WHEN type = 'session.restore_fallback_retired' THEN 1 ELSE 0 END),
  SUM(CASE WHEN type = 'generation.error' THEN 1 ELSE 0 END),
  SUM(CASE WHEN type = 'session.error' THEN 1 ELSE 0 END)
FROM events
WHERE session_id = ?`, session.ID).Scan(&restoreRetiredEvents, &runtimeEvents, &terminalEvents); err != nil {
		t.Fatalf("count restore fallback events: %v", err)
	}
	if restoreRetiredEvents != 1 || runtimeEvents != 1 || terminalEvents != 0 {
		t.Fatalf("unexpected restore fallback events: retired=%d runtime=%d terminal=%d", restoreRetiredEvents, runtimeEvents, terminalEvents)
	}
}

func TestSendMessageRestoreFallbackColdStartFailureLeavesSessionRetryable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_restore_fallback_start_fail", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate checkpointed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark checkpointed generation live: %v", err)
	}
	recordServerRuntimeArtifacts(t, ctx, st, old.GenerationID, "restore_manifest_digest", "runsc restore-test")
	markServerGenerationCheckpointed(t, ctx, st, session.ID, old.GenerationID, time.Now().UTC())
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    restore_ms = 456
WHERE id = ?`, filepath.Join(dir, "checkpoints", session.ID), session.ID); err != nil {
		t.Fatalf("seed checkpoint metadata: %v", err)
	}

	rt := &restoreFailoverRuntime{
		err:     errors.New("checkpoint_runsc_version mismatch"),
		coldErr: errors.New("pre-start sandbox network probe failed"),
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after restore fallback start fail"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body %s", rec.Code, rec.Body.String())
	}
	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get fallback session: %v", err)
	}
	if gotSession.Status != string(sessionstate.RunningIdle) || gotSession.CheckpointPath != "" || gotSession.RestoreMS != nil {
		t.Fatalf("session should stay non-checkpointed and retryable after fallback start failure: %+v", gotSession)
	}
	if gotSession.ActiveGenerationID == "" || gotSession.ActiveGenerationID == old.GenerationID {
		t.Fatalf("expected replacement generation to remain active after failed cold fallback: %+v old=%s", gotSession, old.GenerationID)
	}
	var oldStatus, oldNetwork, oldResources, newStatus, newNetwork, newResources string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, old.GenerationID).Scan(&oldStatus, &oldNetwork, &oldResources); err != nil {
		t.Fatalf("query old generation: %v", err)
	}
	if oldStatus != "failed" || oldNetwork != "reclaimable" || oldResources != "reclaimable" {
		t.Fatalf("old generation not reclaimable after restore fallback: status=%s network=%s resources=%s", oldStatus, oldNetwork, oldResources)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, gotSession.ActiveGenerationID).Scan(&newStatus, &newNetwork, &newResources); err != nil {
		t.Fatalf("query replacement generation: %v", err)
	}
	if newStatus != "failed" || newNetwork != "reclaimable" || newResources != "reclaimable" {
		t.Fatalf("replacement generation should be failed/reclaimable after cold fallback failure: status=%s network=%s resources=%s", newStatus, newNetwork, newResources)
	}
	var queuedTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if queuedTurns != 0 {
		t.Fatalf("turn should not be enqueued when cold fallback start fails, got %d", queuedTurns)
	}
}

func TestSendMessageRestoreLiveCASFailureDestroysRunscContainerIDBeforeFallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_restore_live_cas", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate checkpointed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark checkpointed generation live: %v", err)
	}
	recordServerRuntimeArtifacts(t, ctx, st, old.GenerationID, "restore_manifest_digest", "runsc restore-test")
	markServerGenerationCheckpointed(t, ctx, st, session.ID, old.GenerationID, time.Now().UTC())

	rt := &restoreStartHookRuntime{
		onRestoreStart: func() {
			if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reserved_checkpointed'
WHERE generation_id = ?`, old.GenerationID); err != nil {
				t.Fatalf("force restore live CAS failure: %v", err)
			}
		},
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after restore live cas"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	destroyIDs := rt.runtimeDestroyRequests()
	oldRunscID := serverRunscContainerID(t, ctx, st, session.ID, old.GenerationID)
	if len(destroyIDs) != 1 || destroyIDs[0] != oldRunscID {
		t.Fatalf("restore live CAS cleanup should destroy runsc container id %q, got %+v", oldRunscID, destroyIDs)
	}
	if destroyIDs[0] == session.ID {
		t.Fatalf("restore live CAS cleanup used bare session id %q", session.ID)
	}
	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get fallback session: %v", err)
	}
	if gotSession.ActiveGenerationID == old.GenerationID || gotSession.Status != string(sessionstate.RunningActive) {
		t.Fatalf("fallback did not allocate replacement after restore live CAS failure: %+v old=%s", gotSession, old.GenerationID)
	}
	var oldStatus, oldNetwork, oldResources string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, old.GenerationID).Scan(&oldStatus, &oldNetwork, &oldResources); err != nil {
		t.Fatalf("query old generation: %v", err)
	}
	if oldStatus != "failed" || oldNetwork != "reclaimable" || oldResources != "reclaimable" {
		t.Fatalf("old generation not reclaimable after restore live CAS fallback: status=%s network=%s resources=%s", oldStatus, oldNetwork, oldResources)
	}
}

func TestSendMessageRestoreLiveCASFailureDoesNotRetireWhenDestroyFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_restore_destroy_fail", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate checkpointed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark checkpointed generation live: %v", err)
	}
	recordServerRuntimeArtifacts(t, ctx, st, old.GenerationID, "restore_manifest_digest", "runsc restore-test")
	markServerGenerationCheckpointed(t, ctx, st, session.ID, old.GenerationID, time.Now().UTC())

	rt := &restoreStartHookRuntime{
		recordingRuntime: recordingRuntime{destroyRuntimeErr: errors.New("destroy failed")},
		onRestoreStart: func() {
			if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reserved_checkpointed'
WHERE generation_id = ?`, old.GenerationID); err != nil {
				t.Fatalf("force restore live CAS failure: %v", err)
			}
		},
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after restore destroy fail"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body %s", rec.Code, rec.Body.String())
	}
	destroyIDs := rt.runtimeDestroyRequests()
	oldRunscID := serverRunscContainerID(t, ctx, st, session.ID, old.GenerationID)
	if len(destroyIDs) != 1 || destroyIDs[0] != oldRunscID {
		t.Fatalf("restore cleanup should target runsc container id %q before failing, got %+v", oldRunscID, destroyIDs)
	}
	var oldStatus, oldNetwork, oldResources string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, old.GenerationID).Scan(&oldStatus, &oldNetwork, &oldResources); err != nil {
		t.Fatalf("query old generation: %v", err)
	}
	if oldStatus == "failed" || oldNetwork == "reclaimable" || oldResources == "reclaimable" {
		t.Fatalf("restore generation should not be retired when runtime destroy fails: status=%s network=%s resources=%s", oldStatus, oldNetwork, oldResources)
	}
	var retirementEvents int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM events
WHERE session_id = ?
  AND type IN ('session.restore_fallback_retired', 'generation.error')`, session.ID).Scan(&retirementEvents); err != nil {
		t.Fatalf("count restore fallback events: %v", err)
	}
	if retirementEvents != 0 {
		t.Fatalf("restore fallback events should not be committed when destroy fails, got %d", retirementEvents)
	}
}

func TestSendMessageFallsBackWhenCheckpointImageManifestInvalid(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_restore_manifest_fallback", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	if _, err := st.DBForTest().ExecContext(ctx, `UPDATE sessions SET driver_id = 'sh', mode = 'shell' WHERE id = ?`, session.ID); err != nil {
		t.Fatalf("set shell session agent: %v", err)
	}
	session.Agent = "sh"
	session.Mode = "shell"
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "sh"),
	})
	if err != nil {
		t.Fatalf("allocate checkpointed generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark checkpointed generation live: %v", err)
	}

	checkpointPath := filepath.Join(dir, "checkpoints", session.ID)
	writeServerCheckpointFilesWithoutManifest(t, checkpointPath)
	manifest, err := buildServerCheckpointImageManifest(checkpointPath)
	if err != nil {
		t.Fatalf("build checkpoint image manifest: %v", err)
	}
	if err := writeServerJSONFile(filepath.Join(checkpointPath, "harness-checkpoint-manifest.json"), manifest); err != nil {
		t.Fatalf("write checkpoint image manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkpointPath, "pages.img"), []byte("corrupt"), 0o644); err != nil {
		t.Fatalf("corrupt checkpoint image file: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generation_resources
SET checkpoint_path = ?
WHERE generation_id = ?`, checkpointPath, old.GenerationID); err != nil {
		t.Fatalf("record checkpoint path: %v", err)
	}
	runscPath, runscDigest := currentRunscBinaryMetadataForServerTest()
	recordServerRuntimeArtifactsWithRunsc(t, ctx, st, old.GenerationID, "restore_manifest_digest", "runsc test", runscPath, runscDigest)
	markServerGenerationCheckpointed(t, ctx, st, session.ID, old.GenerationID, time.Now().UTC())

	realRuntime := runtime.New(runtime.Config{
		DefaultAgent:    "sh",
		SessionsRoot:    cfg.SessionsRoot,
		AgentHomesRoot:  filepath.Join(dir, "agent-homes"),
		CheckpointsRoot: filepath.Join(dir, "checkpoints"),
		RootFSPath:      filepath.Join(dir, "rootfs"),
		BundleRoot:      filepath.Join(dir, "run", "runtime"),
		RunscNetwork:    "host",
		RunscOverlay2:   "none",
		RunDir:          cfg.Harness.RunDir,
		CommandRunner:   serverCommandRunner{outputs: map[string][]byte{"runsc --version": []byte("runsc test")}},
		BridgeMode:      "claim-loop",
		BridgeHeartbeat: time.Second,
		SandboxUID:      cfg.Harness.SandboxIdentity.UID,
		SandboxGID:      cfg.Harness.SandboxIdentity.GID,
	})
	rt := &restoreValidationRuntime{restore: realRuntime}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after corrupt checkpoint"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get fallback session: %v", err)
	}
	if gotSession.ActiveGenerationID == "" || gotSession.ActiveGenerationID == old.GenerationID || gotSession.Status != string(sessionstate.RunningActive) {
		t.Fatalf("fallback did not replace active generation: %+v old=%s", gotSession, old.GenerationID)
	}
	var oldStatus, oldNetwork, oldResources, oldReason string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state, COALESCE(g.failure_reason, '')
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, old.GenerationID).Scan(&oldStatus, &oldNetwork, &oldResources, &oldReason); err != nil {
		t.Fatalf("query old generation: %v", err)
	}
	if oldStatus != "failed" || oldNetwork != "reclaimable" || oldResources != "reclaimable" {
		t.Fatalf("old generation not fenced after invalid checkpoint manifest: status=%s network=%s resources=%s reason=%s", oldStatus, oldNetwork, oldResources, oldReason)
	}
	if !strings.Contains(oldReason, "checkpoint image manifest") || !strings.Contains(oldReason, "pages.img") {
		t.Fatalf("old generation failure reason did not include checkpoint manifest mismatch: %q", oldReason)
	}
	var newStatus, newNetwork, newResources string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, gotSession.ActiveGenerationID).Scan(&newStatus, &newNetwork, &newResources); err != nil {
		t.Fatalf("query fallback generation: %v", err)
	}
	if newStatus != "idle" || newNetwork != "live" || newResources != "live" {
		t.Fatalf("fallback generation not live idle: status=%s network=%s resources=%s", newStatus, newNetwork, newResources)
	}
	var queuedTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND status = 'queued'
  AND generation_id IS NULL
  AND content = 'after corrupt checkpoint'`, session.ID).Scan(&queuedTurns); err != nil {
		t.Fatalf("count fallback queued turns: %v", err)
	}
	if queuedTurns != 1 {
		t.Fatalf("queued fallback turn count=%d want 1", queuedTurns)
	}
	if got := len(rt.startRequests); got != 2 {
		t.Fatalf("runtime calls start=%d want 2", got)
	}
	if !rt.startRequests[0].RestoreFromCheckpoint || rt.startRequests[0].GenerationID != old.GenerationID {
		t.Fatalf("first start was not restore: %+v", rt.startRequests[0])
	}
	if rt.startRequests[1].RestoreFromCheckpoint || rt.startRequests[1].GenerationID != gotSession.ActiveGenerationID {
		t.Fatalf("second start was not cold fallback: %+v", rt.startRequests[1])
	}
}

func TestColdFallbackMaintenanceStartsReplacementForQueuedTurn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_maintenance_fallback", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate old generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark old generation live: %v", err)
	}
	if err := st.FailGeneration(ctx, store.FailGenerationParams{
		SessionID:    session.ID,
		GenerationID: old.GenerationID,
		Owner:        old.Owner,
		ErrorClass:   "orchestrator_restart_reconnect_grace_expired",
		Reason:       "orchestrator_restart_reconnect_grace_expired",
		Now:          time.Now().UTC(),
	}); err != nil {
		t.Fatalf("fail old generation: %v", err)
	}
	turnID, err := st.EnqueueTurn(ctx, session.ID, "retry old queued turn", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}

	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	srv.startColdFallbackSessions(ctx, leaseOwner)

	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.Status != string(sessionstate.RunningActive) ||
		gotSession.ActiveGenerationID == "" ||
		gotSession.ActiveGenerationID == old.GenerationID {
		t.Fatalf("unexpected fallback session state: %+v old=%s", gotSession, old.GenerationID)
	}
	_, startRequests := rt.requests()
	if len(startRequests) != 1 ||
		startRequests[0].GenerationID != gotSession.ActiveGenerationID {
		t.Fatalf("unexpected maintenance start requests: %+v", startRequests)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{
		SessionID:    session.ID,
		GenerationID: gotSession.ActiveGenerationID,
		Owner:        leaseOwner,
		RequestID:    "req_fallback_claim",
		LeaseTTL:     time.Minute,
		Now:          time.Now().UTC(),
	})
	if err != nil || !ok {
		t.Fatalf("claim queued fallback turn: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	if grant.TurnID != turnID || grant.Content != "retry old queued turn" {
		t.Fatalf("claimed wrong fallback turn: %+v want turn %d", grant, turnID)
	}
}

func TestColdFallbackMaintenanceStartFailureKeepsSessionInputBlocking(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_maintenance_fallback_fail", string(sessionstate.RunningActive), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate old generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, old.GenerationID, old.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark old generation live: %v", err)
	}
	if err := st.FailGeneration(ctx, store.FailGenerationParams{
		SessionID:    session.ID,
		GenerationID: old.GenerationID,
		Owner:        old.Owner,
		ErrorClass:   "orchestrator_restart_reconnect_grace_expired",
		Reason:       "orchestrator_restart_reconnect_grace_expired",
		Now:          time.Now().UTC(),
	}); err != nil {
		t.Fatalf("fail old generation: %v", err)
	}
	turnID, err := st.EnqueueTurn(ctx, session.ID, "protected queued turn", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}

	hub := events.NewHub()
	eventsCh, cancelEvents := hub.Subscribe(session.ID)
	defer cancelEvents()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: failingRuntime{err: errors.New("pre-start sandbox network probe failed")},
		hub:     hub,
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	srv.startColdFallbackSessions(ctx, leaseOwner)

	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.Status != string(sessionstate.RunningActive) ||
		sessionstate.CanAcceptInput(gotSession.Status) ||
		gotSession.ActiveGenerationID == "" ||
		gotSession.ActiveGenerationID == old.GenerationID {
		t.Fatalf("unexpected fallback-failure session state: %+v old=%s", gotSession, old.GenerationID)
	}
	var generationStatus, errorClass, networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, gotSession.ActiveGenerationID).Scan(&generationStatus, &errorClass, &networkState, &resourceState); err != nil {
		t.Fatalf("query failed fallback generation: %v", err)
	}
	if generationStatus != "failed" ||
		errorClass != "probe_failed_pre_start" ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" {
		t.Fatalf("unexpected failed fallback generation: status=%s class=%s network=%s resource=%s", generationStatus, errorClass, networkState, resourceState)
	}
	var queuedStatus string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT status
FROM turns
WHERE id = ?`, turnID).Scan(&queuedStatus); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if queuedStatus != "queued" {
		t.Fatalf("queued turn status=%s want queued", queuedStatus)
	}
	seenGenerationError := false
	for {
		select {
		case event := <-eventsCh:
			switch event.Type {
			case "generation.error":
				seenGenerationError = true
			case "session." + string(sessionstate.RunningIdle), "session." + string(sessionstate.Failed), "session.error":
				t.Fatalf("unexpected terminal/input-acceptable event after fallback failure: %+v", event)
			}
		default:
			if !seenGenerationError {
				t.Fatalf("missing generation.error event")
			}
			return
		}
	}
}

func TestRunMaintenanceRetiresExpiredCheckpoint(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_retire_checkpoint", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Reaper.CheckpointImageRetention = config.Duration{Duration: 0}
	cfg.Harness.Reaper.FailedRetention = config.Duration{Duration: time.Hour}
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)
	checkpointedAt := time.Now().UTC().Add(-2 * time.Hour)
	markServerGenerationCheckpointed(t, ctx, st, session.ID, allocation.GenerationID, checkpointedAt)
	checkpointPath := filepath.Join(dir, "checkpoint", session.ID)
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    restore_ms = 99,
    last_activity_at = ?
WHERE id = ?`, checkpointPath, checkpointedAt.Format(time.RFC3339Nano), session.ID); err != nil {
		t.Fatalf("seed checkpoint session metadata: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generation_resources
SET checkpoint_path = ?
WHERE generation_id = ?`, checkpointPath, allocation.GenerationID); err != nil {
		t.Fatalf("seed checkpoint resource path: %v", err)
	}

	hub := events.NewHub()
	eventsCh, cancelEvents := hub.Subscribe(session.ID)
	defer cancelEvents()
	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, hub),
		hub:     hub,
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()
	event := waitForHubEvent(t, eventsCh, "session.checkpoint_retired")
	waitForGenerationResourceStates(t, runCtx, st, allocation.GenerationID, "destroyed", "destroyed")
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance exit err=%v, want context canceled", err)
	}
	payload, ok := event.Payload.(json.RawMessage)
	if !ok || strings.Contains(string(payload), `"checkpoint_path"`) || !strings.Contains(string(payload), `"restore_ms":null`) {
		t.Fatalf("unexpected checkpoint retirement event payload: %#v", event.Payload)
	}

	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get retired session: %v", err)
	}
	if gotSession.Status != string(sessionstate.RunningIdle) || gotSession.CheckpointPath != "" || gotSession.RestoreMS != nil {
		t.Fatalf("unexpected retired session: %+v", gotSession)
	}
	var generationStatus, generationError, networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &generationError, &networkState, &resourceState); err != nil {
		t.Fatalf("query retired generation: %v", err)
	}
	if generationStatus != "failed" || generationError != "checkpoint_retired" || networkState != "destroyed" || resourceState != "destroyed" {
		t.Fatalf("unexpected retired generation: status=%s error=%s network=%s resource=%s", generationStatus, generationError, networkState, resourceState)
	}
	destroyRequests := rt.destroyGenerationRequests()
	if len(destroyRequests) != 1 || destroyRequests[0].GenerationID != allocation.GenerationID {
		t.Fatalf("unexpected destroy generation requests: %+v", destroyRequests)
	}
}

func TestReleasedIdleSessionColdStartsOnWake(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_released_wake", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	enableSessionAutoCheckpoint(t, ctx, st, session.ID)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.243.0.0/28")}
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)

	// Release the idle sandbox (no checkpoint image).
	if err := st.ReleaseIdleGeneration(ctx, session.ID, allocation.GenerationID, store.GenerationLeaseOwner(owner.UUID), time.Now().UTC()); err != nil {
		t.Fatalf("release idle generation: %v", err)
	}

	released, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get released session: %v", err)
	}
	if released.Status != string(sessionstate.Checkpointed) {
		t.Fatalf("released session status=%s want checkpointed", released.Status)
	}

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: &recordingRuntime{},
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	// Waking a released session must cold-start a fresh generation, never a
	// checkpoint restore (there is no image).
	ensured, err := srv.ensureActiveGeneration(ctx, released, store.GenerationLeaseOwner(owner.UUID))
	if err != nil {
		t.Fatalf("ensure active generation on wake: %v", err)
	}
	if !ensured.IsNew || ensured.RestoreFromCheckpoint || ensured.Allocation.GenerationID == allocation.GenerationID {
		t.Fatalf("wake should cold-start a replacement generation: %+v old=%s", ensured, allocation.GenerationID)
	}
}

func TestEnsureActiveGenerationColdStartsAfterCheckpointRetirement(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_retire_then_send", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/28")}
	allocation := prepareServerIdleGeneration(t, ctx, st, cfg, owner.UUID, session.ID)
	checkpointedAt := time.Now().UTC().Add(-2 * time.Hour)
	markServerGenerationCheckpointed(t, ctx, st, session.ID, allocation.GenerationID, checkpointedAt)
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE sessions
SET checkpoint_path = ?,
    last_activity_at = ?
WHERE id = ?`, filepath.Join(dir, "checkpoint", session.ID), checkpointedAt.Format(time.RFC3339Nano), session.ID); err != nil {
		t.Fatalf("seed checkpoint session metadata: %v", err)
	}
	staleCheckpointedSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get checkpointed session: %v", err)
	}
	if _, err := st.RetireExpiredCheckpoints(ctx, store.RetireExpiredCheckpointsParams{
		OwnerUUID:                owner.UUID,
		Now:                      time.Now().UTC(),
		CheckpointImageRetention: time.Hour,
	}); err != nil {
		t.Fatalf("retire checkpoint: %v", err)
	}

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: &recordingRuntime{},
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	ensured, err := srv.ensureActiveGeneration(ctx, staleCheckpointedSession, store.GenerationLeaseOwner(owner.UUID))
	if err != nil {
		t.Fatalf("ensure active generation after retirement: %v", err)
	}
	if !ensured.IsNew || ensured.RestoreFromCheckpoint || ensured.Allocation.GenerationID == allocation.GenerationID {
		t.Fatalf("ensure should cold-start replacement after checkpoint retirement: %+v old=%s", ensured, allocation.GenerationID)
	}
	gotSession, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get replacement session: %v", err)
	}
	if gotSession.ActiveGenerationID != ensured.Allocation.GenerationID {
		t.Fatalf("session active generation=%s want replacement %s", gotSession.ActiveGenerationID, ensured.Allocation.GenerationID)
	}
}

func TestGetQuotaReportsSessionAndPoolCeilings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	createServerTestSession(t, ctx, st, dir, "sess_quota", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.MaxSessions = 3
	cfg.Harness.MaxSessions = 3
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.242.0.0/29")}
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: "sess_quota",
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config: store.ResourceAllocatorConfig{
			RunDir:             cfg.Harness.RunDir,
			CIDRPool:           cfg.Harness.Network.CIDRPool.Prefix,
			EgressDorisFEHosts: cfg.Harness.Network.Egress.DorisFEHosts,
			EgressDorisBEHosts: cfg.Harness.Network.Egress.DorisBEHosts,
			EgressDorisPorts:   cfg.Harness.Network.Egress.DorisPorts,
			EgressDNSPolicy:    string(cfg.Harness.Network.Egress.DNSPolicy),
			HostProxyBindURL:   cfg.ModelProxy.BindURL,
			ProxyPort:          cfg.ModelProxy.BindPort,
			Agent:              "pi",
			AgentModel:         "sonnet",
			AgentOutputFormat:  agents.PiEventSchemaVersion,
		},
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode quota: %v", err)
	}
	if body["soft_session_ceiling"] != 3 ||
		body["active_sessions"] != 1 ||
		body["live_pool_ceiling"] != 2 ||
		body["allocated_pool_slots"] != 1 ||
		body["remaining_pool_slots"] != 1 {
		t.Fatalf("unexpected quota body for allocation %s: %+v", allocation.GenerationID, body)
	}
	if _, ok := body["effective_ceiling"]; ok {
		t.Fatalf("quota should report session and pool ceilings separately without effective_ceiling: %+v", body)
	}
}

func TestSendMessagePoolExhaustionDoesNotQueueTurn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.242.0.0/30")}
	createServerTestSession(t, ctx, st, dir, "sess_pool_used", string(sessionstate.Created), time.Now().UTC(), nil)
	target := createServerTestSession(t, ctx, st, dir, "sess_pool_target", string(sessionstate.Created), time.Now().UTC(), nil)
	if _, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: "sess_pool_used",
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	}); err != nil {
		t.Fatalf("allocate pool slot: %v", err)
	}

	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+target.ID+"/messages", strings.NewReader(`{"content":"hello"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, target.ID)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error_class"] != "pool_exhausted" {
		t.Fatalf("expected pool_exhausted, got %v", body)
	}
	var targetGenerations, targetTurns int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_generations WHERE session_id = ?`, target.ID).Scan(&targetGenerations); err != nil {
		t.Fatalf("count target generations: %v", err)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ?`, target.ID).Scan(&targetTurns); err != nil {
		t.Fatalf("count target turns: %v", err)
	}
	if targetGenerations != 0 || targetTurns != 0 {
		t.Fatalf("pool exhaustion leaked target state: generations=%d turns=%d", targetGenerations, targetTurns)
	}
}

func TestSendMessageRuntimeStartFailureMarksGenerationFailedAndReclaimable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_runtime_fail", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	srv := &Server{
		cfg:   cfg,
		store: st,
		runtime: failingRuntime{
			err: errors.New("pre-start sandbox network probe failed"),
		},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"hello"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error_class"] != "probe_failed_pre_start" ||
		body["error"] != "sandbox network probe failed before start" {
		t.Fatalf("unexpected response body: %v", body)
	}
	var generationStatus, errorClass, networkState, resourceState, sessionStatus, sessionErrorClass, sessionFailureReason string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state, s.status,
       COALESCE(s.error_class, ''), COALESCE(s.failure_reason, '')
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN sessions s ON s.active_generation_id = g.generation_id
WHERE g.session_id = ?`, session.ID).Scan(
		&generationStatus,
		&errorClass,
		&networkState,
		&resourceState,
		&sessionStatus,
		&sessionErrorClass,
		&sessionFailureReason,
	); err != nil {
		t.Fatalf("query generation state: %v", err)
	}
	if generationStatus != "failed" ||
		errorClass != "probe_failed_pre_start" ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" ||
		sessionStatus != string(sessionstate.Created) ||
		sessionErrorClass != "" ||
		sessionFailureReason != "" {
		t.Fatalf("unexpected failed generation state: generation=%s class=%s network=%s resource=%s session=%s session_class=%s session_reason=%s", generationStatus, errorClass, networkState, resourceState, sessionStatus, sessionErrorClass, sessionFailureReason)
	}
	if !sessionstate.CanAcceptInput(sessionStatus) {
		t.Fatalf("session should remain input-acceptable after start failure, got %s", sessionStatus)
	}
	var runtimeResourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT state
FROM runtime_resource_instances
WHERE generation_id = (
  SELECT generation_id FROM runtime_generations WHERE session_id = ?
)`, session.ID).Scan(&runtimeResourceState); err != nil {
		t.Fatalf("query runtime resource instance after start failure: %v", err)
	}
	if runtimeResourceState != string(store.RuntimeResourceRetiring) {
		t.Fatalf("runtime resource state after start failure=%s want %s", runtimeResourceState, store.RuntimeResourceRetiring)
	}
	var runtimeEvents int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM events
WHERE session_id = ?
  AND type = 'generation.error'`, session.ID).Scan(&runtimeEvents); err != nil {
		t.Fatalf("count generation error events: %v", err)
	}
	if runtimeEvents != 1 {
		t.Fatalf("expected one generation.error event, got %d", runtimeEvents)
	}
	var turns int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ?`, session.ID).Scan(&turns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turns != 0 {
		t.Fatalf("runtime start failure should happen before turn creation, got %d turns", turns)
	}
	var failedGenerationID string
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT generation_id FROM runtime_generations WHERE session_id = ?`, session.ID).Scan(&failedGenerationID); err != nil {
		t.Fatalf("query failed generation id: %v", err)
	}
	if err := srv.cleanupGenerationResources(ctx, session.ID, failedGenerationID, time.Now().UTC()); err != nil {
		t.Fatalf("cleanup failed generation resources: %v", err)
	}
	instance, err := st.GetRuntimeResourceInstance(ctx, failedGenerationID)
	if err != nil {
		t.Fatalf("get cleaned runtime resource instance: %v", err)
	}
	if instance.State != store.RuntimeResourceDestroyed ||
		instance.EvidenceDigest == "" ||
		len(instance.EvidenceJSON) == 0 ||
		instance.VerifiedAt == nil {
		t.Fatalf("runtime resource cleanup did not record destroyed evidence: %+v", instance)
	}
	var evidence store.ResourceReconciliationEvidence
	if err := json.Unmarshal(instance.EvidenceJSON, &evidence); err != nil {
		t.Fatalf("decode runtime resource cleanup evidence: %v", err)
	}
	if !strings.HasPrefix(evidence.RunscState, "runsc_container:absent") {
		t.Fatalf("runtime resource cleanup did not record runsc absence: %+v", evidence)
	}

	srv.runtime = instantRuntime{}
	retryReq := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"retry"}`))
	retryRec := httptest.NewRecorder()
	srv.sendMessage(retryRec, retryReq, session.ID)
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("expected retry status 202, got %d body %s", retryRec.Code, retryRec.Body.String())
	}
	var generationCount int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_generations WHERE session_id = ?`, session.ID).Scan(&generationCount); err != nil {
		t.Fatalf("count generations after retry: %v", err)
	}
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ?`, session.ID).Scan(&turns); err != nil {
		t.Fatalf("count turns after retry: %v", err)
	}
	if generationCount != 2 || turns != 1 {
		t.Fatalf("retry should allocate generation N+1 and enqueue one turn, generations=%d turns=%d", generationCount, turns)
	}
}

func TestStartEnsuredGenerationRenewsLeaseDuringSlowPrepare(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_slow_start", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	// Short enough that the background renewal loop (ticks at ttl/2) fires well
	// within this test's 1s wait budgets, but long enough that the synchronous
	// chain of DB writes between claiming the runtime resource materialization
	// lease and its first renewal doesn't race the lease's own expiry under a
	// loaded CI runner.
	cfg.Harness.Bridge.LeaseTTL = config.Duration{Duration: 500 * time.Millisecond}
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  cfg.Harness.Bridge.LeaseTTL.Duration,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	rt := newBlockingPrepareRuntime()
	t.Cleanup(rt.release)
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.startEnsuredGeneration(ctx, session, ensuredGeneration{
			Allocation: allocation,
			IsNew:      true,
		}, startFailureInputAcceptable)
	}()

	select {
	case <-rt.prepareStarted:
	case <-time.After(time.Second):
		t.Fatalf("prepare did not start")
	}
	waitForGenerationLeaseAfter(t, ctx, st, allocation.GenerationID, allocation.LeaseExpiresAt)
	rt.release()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("start ensured generation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("start ensured generation did not finish")
	}
	waitForGenerationStatus(t, ctx, st, allocation.GenerationID, "idle")
	volumeConfig, err := srv.dataVolumeProvisionerConfig()
	if err != nil {
		t.Fatalf("data volume config: %v", err)
	}
	workspaceVolume, err := (localworkspace.Volumes{Records: st}).VerifySessionWorkspaceVolume(ctx, localworkspace.VerifySessionWorkspaceVolumeParams{
		SessionID: session.ID,
		Config:    volumeConfig,
	})
	if err != nil {
		t.Fatalf("verify workspace volume: %v", err)
	}
	driverHomeVolume, err := (localworkspace.Volumes{Records: st}).VerifySessionDriverHomeVolume(ctx, localworkspace.VerifySessionDriverHomeVolumeParams{
		SessionID: session.ID,
		Driver:    session.Agent,
		Config:    volumeConfig,
	})
	if err != nil {
		t.Fatalf("verify driver home volume: %v", err)
	}
	prepares, starts := rt.requests()
	if len(prepares) != 1 || len(starts) != 1 {
		t.Fatalf("runtime requests prepare=%d start=%d", len(prepares), len(starts))
	}
	if prepares[0].WorkspaceHostPath != workspaceVolume.HostPath ||
		prepares[0].AgentHomeHostPath != driverHomeVolume.HostPath ||
		starts[0].WorkspaceHostPath != workspaceVolume.HostPath ||
		starts[0].AgentHomeHostPath != driverHomeVolume.HostPath {
		t.Fatalf("runtime did not receive data volume paths: prepare=%+v start=%+v workspace=%+v home=%+v",
			prepares[0], starts[0], workspaceVolume, driverHomeVolume)
	}
	instance, err := st.GetRuntimeResourceInstance(ctx, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime resource instance: %v", err)
	}
	if instance.State != store.RuntimeResourceLive ||
		instance.WorkerID != owner.UUID ||
		instance.RunscContainerID != serverRunscContainerID(t, ctx, st, session.ID, allocation.GenerationID) ||
		instance.RunscBinaryPath != "/usr/local/bin/runsc-test" ||
		instance.RunscBinaryDigest != "sha256:runsc-test" ||
		instance.NftTableName != runtimeResourceNftTableName(allocation.GenerationID) {
		t.Fatalf("unexpected runtime resource instance: %+v", instance)
	}
	contract, err := st.GetSandboxContractForGeneration(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get sandbox contract: %v", err)
	}
	if contract.SandboxContractVersion != store.SandboxContractVersion ||
		contract.ContractID != sandboxContractID(allocation.GenerationID) ||
		contract.ContractGateVersion != store.SandboxContractGateDriverManifest {
		t.Fatalf("unexpected sandbox contract: %+v", contract)
	}
	var payload map[string]any
	if err := json.Unmarshal(contract.CanonicalPayload, &payload); err != nil {
		t.Fatalf("decode sandbox contract payload: %v", err)
	}
	if payload["contract_gate_version"] != store.SandboxContractGateDriverManifest {
		t.Fatalf("sandbox contract gate version should be driver_manifest_v1: %s", contract.CanonicalPayload)
	}
	inputDigests, ok := payload["input_digests"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox contract missing input digests: %s", contract.CanonicalPayload)
	}
	for _, key := range []string{"runtime_config_digest", "agent_manifest_digest"} {
		value, _ := inputDigests[key].(string)
		if !strings.HasPrefix(value, "sha256:") {
			t.Fatalf("sandbox contract missing %s: %s", key, contract.CanonicalPayload)
		}
	}
	evidence, err := st.GetSandboxContractInputEvidence(ctx, contract.ContractID)
	if err != nil {
		t.Fatalf("get sandbox contract input evidence: %v", err)
	}
	if evidence.RuntimeConfigDigest != inputDigests["runtime_config_digest"] ||
		evidence.AgentManifestDigest != inputDigests["agent_manifest_digest"] ||
		!json.Valid(evidence.RuntimeConfigPreimage) ||
		!json.Valid(evidence.AgentManifestPayload) {
		t.Fatalf("sandbox contract input evidence mismatch: evidence=%+v input=%+v", evidence, inputDigests)
	}
	if inputDigests["rootfs_image_digest"] != nil {
		t.Fatalf("rootfs digest should remain null until rootfs evidence is available: %s", contract.CanonicalPayload)
	}
	adapter, ok := payload["runtime_adapter"].(map[string]any)
	if !ok || adapter["runsc_container_id"] != serverRunscContainerID(t, ctx, st, session.ID, allocation.GenerationID) {
		t.Fatalf("sandbox contract missing runsc identity: %s", contract.CanonicalPayload)
	}
	if adapter["runsc_binary_path"] != "/usr/local/bin/runsc-test" ||
		adapter["runsc_binary_digest"] != "sha256:runsc-test" {
		t.Fatalf("sandbox contract missing runsc binary metadata: %s", contract.CanonicalPayload)
	}
	ambientCaps, ok := adapter["ambient_capabilities"].([]any)
	if adapter["no_new_privileges"] != true || !ok || len(ambientCaps) != 0 {
		t.Fatalf("sandbox contract missing runtime capability policy: %s", contract.CanonicalPayload)
	}
	forbiddenCaps, ok := adapter["forbidden_capabilities"].([]any)
	if !ok || !jsonArrayContainsAll(forbiddenCaps, "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_SYS_ADMIN") {
		t.Fatalf("sandbox contract missing forbidden capability policy: %s", contract.CanonicalPayload)
	}
	requiredAnnotations, ok := adapter["required_annotations"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox contract missing required annotations: %s", contract.CanonicalPayload)
	}
	bridgeAnnotations, ok := requiredAnnotations[bridge.BridgeMountDestination].(map[string]any)
	if !ok ||
		bridgeAnnotations["dev.gvisor.spec.mount./harness-control/bridge.type"] != "bind" ||
		bridgeAnnotations["dev.gvisor.spec.mount./harness-control/bridge.share"] != "exclusive" {
		t.Fatalf("sandbox contract missing bridge required annotation policy: %s", contract.CanonicalPayload)
	}
	networkIdentity, ok := payload["network_identity"].(map[string]any)
	if !ok ||
		networkIdentity["sandbox_ip"] != instance.SandboxIP ||
		networkIdentity["nft_table_name"] != instance.NftTableName {
		t.Fatalf("sandbox contract missing runtime network identity: %s instance=%+v", contract.CanonicalPayload, instance)
	}
	resourceIdentity, ok := payload["resource_identity"].(map[string]any)
	if !ok || resourceIdentity["resource_identity_digest"] != instance.ResourceIdentityDigest {
		t.Fatalf("sandbox contract missing resource identity digest: %s instance=%+v", contract.CanonicalPayload, instance)
	}
	mountPlan, ok := payload["mount_plan"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox contract missing mount plan: %s", contract.CanonicalPayload)
	}
	workspaceMount, ok := mountPlan["workspace"].(map[string]any)
	if !ok || workspaceMount["source"] != workspaceVolume.HostPath {
		t.Fatalf("sandbox contract workspace mount does not use data volume: %s", contract.CanonicalPayload)
	}
	agentHomeMount, ok := mountPlan["agent_home"].(map[string]any)
	if !ok || agentHomeMount["source"] != driverHomeVolume.HostPath {
		t.Fatalf("sandbox contract agent home mount does not use data volume: %s", contract.CanonicalPayload)
	}
	dataVolumes, ok := payload["data_volumes"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox contract missing data volume ownership: %s", contract.CanonicalPayload)
	}
	workspacePayload, ok := dataVolumes["workspace"].(map[string]any)
	if !ok || workspacePayload["provisioning_marker_digest"] != workspaceVolume.ProvisioningMarkerDigest {
		t.Fatalf("sandbox contract workspace data volume evidence mismatch: %s", contract.CanonicalPayload)
	}
	driverHomePayload, ok := dataVolumes["agent_home"].(map[string]any)
	if !ok || driverHomePayload["provisioning_marker_digest"] != driverHomeVolume.ProvisioningMarkerDigest {
		t.Fatalf("sandbox contract driver home data volume evidence mismatch: %s", contract.CanonicalPayload)
	}
	var manifestDigest, specDigest, bundleDigest string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT control_manifest_digest, oci_spec_digest, bundle_digest
FROM sandbox_contract_artifacts
WHERE contract_id = ?`, contract.ContractID).Scan(&manifestDigest, &specDigest, &bundleDigest); err != nil {
		t.Fatalf("query sandbox contract artifacts: %v", err)
	}
	if manifestDigest != "manifest_digest" || specDigest != "spec_digest" || bundleDigest != "bundle_digest" {
		t.Fatalf("unexpected sandbox contract artifact digests: manifest=%s spec=%s bundle=%s", manifestDigest, specDigest, bundleDigest)
	}
}

func TestSandboxContractPayloadRecordsPiMaterializedConfig(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	session := store.Session{
		ID:        "sess_pi_contract",
		UserID:    labUserID,
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Mode:      "agent",
		Workspace: filepath.Join(dir, "sessions", "sess_pi_contract"),
		RestoreID: "pi-driver",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create pi session: %v", err)
	}
	allocatorConfig := store.ResourceAllocatorConfig{
		RunDir:                     filepath.Join(dir, "run"),
		CIDRPool:                   netip.MustParsePrefix("10.240.0.0/28"),
		EgressDNSPolicy:            "hostnames_only",
		HostProxyBindURL:           "http://0.0.0.0:8082",
		ProxyPort:                  8082,
		Agent:                      "pi",
		AgentModel:                 "sonnet",
		AgentOutputFormat:          "pi_rpc_events_v1.0",
		DisableNonessentialTraffic: true,
		SandboxUID:                 65534,
		SandboxGID:                 65534,
		SandboxModelProxyBaseURL:   "http://harness-model-proxy.internal:8082",
	}
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    allocatorConfig,
	})
	if err != nil {
		t.Fatalf("allocate pi generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	rt := runtime.New(runtime.Config{
		BundleRoot:         filepath.Join(dir, "bundle", "out"),
		RootFSPath:         filepath.Join(dir, "rootfs"),
		BridgeHeartbeat:    20 * time.Second,
		BridgePollInterval: 5 * time.Millisecond,
		CommandRunner: serverCommandRunner{outputs: map[string][]byte{
			"ip netns exec " + details.NetnsName + " curl -sS --max-time 2 -o /dev/null -w %{http_code} " + strings.TrimRight(details.ProbeURL, "/") + "/healthz": []byte("200"),
		}},
	})
	workspaceHostPath := filepath.Join(dir, "volumes", "workspaces", session.ID)
	agentHomeHostPath := filepath.Join(dir, "volumes", "driver-homes", session.ID, "pi")
	artifacts, err := rt.PrepareGeneration(ctx, runtime.StartRequest{
		SessionID:         session.ID,
		GenerationID:      allocation.GenerationID,
		Agent:             "pi",
		Generation:        details,
		WorkspaceHostPath: workspaceHostPath,
		AgentHomeHostPath: agentHomeHostPath,
	})
	if err != nil {
		t.Fatalf("prepare pi runtime artifacts: %v", err)
	}
	srv := &Server{cfg: config.Config{DefaultAgent: "pi"}, store: st, runtime: rt}
	payload, err := srv.sandboxContractPayload(session, details, artifacts, "sha256:resource-identity", sessionRuntimeDataVolumes{
		Workspace: store.SessionWorkspaceVolume{
			SessionID:                session.ID,
			HostPath:                 workspaceHostPath,
			LayoutVersion:            localworkspace.DataVolumeLayoutVersion,
			RuntimeIdentityDigest:    "sha256:workspace-identity",
			ProvisioningMarkerPath:   filepath.Join(dir, "evidence", "workspaces", session.ID+".json"),
			ProvisioningMarkerDigest: "sha256:workspace-marker",
		},
		DriverHome: store.SessionDriverHomeVolume{
			SessionID:                session.ID,
			Driver:                   "pi",
			HostPath:                 agentHomeHostPath,
			LayoutVersion:            localworkspace.DataVolumeLayoutVersion,
			RuntimeIdentityDigest:    "sha256:driver-home-identity",
			ProvisioningMarkerPath:   filepath.Join(dir, "evidence", "driver-homes", session.ID, "pi.json"),
			ProvisioningMarkerDigest: "sha256:driver-home-marker",
		},
	})
	if err != nil {
		t.Fatalf("build pi sandbox contract payload: %v", err)
	}
	mountPlan := payload["mount_plan"].(map[string]any)
	materializedMounts := mountPlan["driver_config_materializations"].(map[string]any)
	driverRuntime := payload["driver_runtime"].(map[string]any)
	materializedRuntime := driverRuntime["materialized_driver_config"].(map[string]any)
	for _, name := range []string{"models", "settings"} {
		runtimeEntry := materializedRuntime[name].(map[string]any)
		mountEntry := materializedMounts[name].(map[string]any)
		if runtimeEntry["destination_mutable_by_sandbox"] != false ||
			mountEntry["destination_mutable_by_sandbox"] != false ||
			mountEntry["mode"] != "ro" ||
			mountEntry["type"] != "bind" ||
			mountEntry["exact"] != true {
			t.Fatalf("pi materialization %s is not read-only/exact: runtime=%+v mount=%+v", name, runtimeEntry, mountEntry)
		}
		if digest, _ := runtimeEntry["source_digest"].(string); !strings.HasPrefix(digest, "sha256:") {
			t.Fatalf("pi materialization %s missing digest: %+v", name, runtimeEntry)
		}
	}
	if materializedRuntime["models"].(map[string]any)["source_projection_path"] != agents.PiModelsConfigPath ||
		materializedRuntime["settings"].(map[string]any)["source_projection_path"] != agents.PiSettingsConfigPath {
		t.Fatalf("pi materialization source projections wrong: %+v", materializedRuntime)
	}
	if _, err := st.StoreSandboxContract(ctx, store.StoreSandboxContractParams{
		ContractID:             sandboxContractID(allocation.GenerationID),
		SessionID:              session.ID,
		GenerationID:           allocation.GenerationID,
		Owner:                  store.GenerationLeaseOwner(owner.UUID),
		SandboxContractVersion: store.SandboxContractVersion,
		ContractSchemaVersion:  store.SandboxContractSchemaVersion,
		ContractGateVersion:    store.SandboxContractGateDriverManifest,
		DriverState:            allocation.DriverState,
		Payload:                payload,
		Now:                    now,
	}); err != nil {
		t.Fatalf("store pi sandbox contract: %v", err)
	}
}

func TestStartEnsuredGenerationLeavesBridgeClaimsUntilLivePoll(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_start_claim_deferred", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	rt := &claimAfterProbeRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	if err := srv.startEnsuredGeneration(ctx, session, ensuredGeneration{
		Allocation: allocation,
		IsNew:      true,
	}, startFailureInputAcceptable); err != nil {
		t.Fatalf("start ensured generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("generation details: %v", err)
	}
	outbox, err := bridge.OpenQueue(details.BridgeDirPath, bridge.OutboxDir)
	if err != nil {
		t.Fatalf("open bridge outbox: %v", err)
	}
	files, err := outbox.ReadAll()
	if err != nil {
		t.Fatalf("read bridge outbox: %v", err)
	}
	if len(files) != 1 || files[0].Envelope.Type != bridge.TypeClaimNextTurn {
		t.Fatalf("startup probe should leave only claim for live poller, got %+v", files)
	}
}

func TestRuntimeResourceInstanceCheckpointRestoreTransitions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_resource_checkpoint_restore", string(sessionstate.Created), time.Now().UTC(), nil)
	enableSessionAutoCheckpoint(t, ctx, st, session.ID)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	if err := srv.startEnsuredGeneration(ctx, session, ensuredGeneration{Allocation: allocation, IsNew: true}, startFailureInputAcceptable); err != nil {
		t.Fatalf("start ensured generation: %v", err)
	}
	if err := st.UpdateSessionStatusAndActivity(ctx, session.ID, string(sessionstate.RunningIdle), nil, time.Now().UTC()); err != nil {
		t.Fatalf("mark session idle: %v", err)
	}
	testDriveCheckpoint(t, ctx, srv, st, session.ID, allocation.GenerationID, allocation.Owner)
	instance, err := st.GetRuntimeResourceInstance(ctx, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get checkpointed runtime resource: %v", err)
	}
	if instance.State != store.RuntimeResourceCheckpointReserved {
		t.Fatalf("runtime resource after checkpoint=%s want %s", instance.State, store.RuntimeResourceCheckpointReserved)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"after checkpoint"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected restore status 202, got %d body %s", rec.Code, rec.Body.String())
	}
	instance, err = st.GetRuntimeResourceInstance(ctx, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get restored runtime resource: %v", err)
	}
	if instance.State != store.RuntimeResourceLive ||
		instance.WorkerID != owner.UUID ||
		instance.IdempotencyToken != "" ||
		instance.LeaseExpiresAt != nil {
		t.Fatalf("unexpected runtime resource after restore: %+v", instance)
	}
	_, starts := rt.requests()
	if len(starts) != 2 || !starts[1].RestoreFromCheckpoint {
		t.Fatalf("expected second start to restore checkpoint, got %+v", starts)
	}
	if starts[1].Generation.RunscContainerID != instance.RunscContainerID ||
		starts[1].Generation.RunscBinaryDigest != instance.RunscBinaryDigest ||
		starts[1].Generation.NetnsName != instance.NetnsName {
		t.Fatalf("restore start did not use runtime resource identity: start=%+v instance=%+v", starts[1].Generation, instance)
	}
	volumeConfig, err := srv.dataVolumeProvisionerConfig()
	if err != nil {
		t.Fatalf("data volume config: %v", err)
	}
	workspaceVolume, err := (localworkspace.Volumes{Records: st}).VerifySessionWorkspaceVolume(ctx, localworkspace.VerifySessionWorkspaceVolumeParams{
		SessionID: session.ID,
		Config:    volumeConfig,
	})
	if err != nil {
		t.Fatalf("verify workspace volume: %v", err)
	}
	driverHomeVolume, err := (localworkspace.Volumes{Records: st}).VerifySessionDriverHomeVolume(ctx, localworkspace.VerifySessionDriverHomeVolumeParams{
		SessionID: session.ID,
		Driver:    session.Agent,
		Config:    volumeConfig,
	})
	if err != nil {
		t.Fatalf("verify driver home volume: %v", err)
	}
	if starts[1].WorkspaceHostPath != workspaceVolume.HostPath ||
		starts[1].AgentHomeHostPath != driverHomeVolume.HostPath {
		t.Fatalf("restore start did not use data volume paths: start=%+v workspace=%+v home=%+v", starts[1], workspaceVolume, driverHomeVolume)
	}
}

func TestStartEnsuredGenerationDestroysRuntimeAfterOwnerLoss(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_start_owner_loss", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	rt := &startHookRuntime{
		onStart: func(req runtime.StartRequest) {
			if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = 'other_owner',
    lease_expires_at = ?
WHERE generation_id = ?`, time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), req.GenerationID); err != nil {
				t.Fatalf("steal generation lease: %v", err)
			}
		},
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}

	err = srv.startEnsuredGeneration(ctx, session, ensuredGeneration{
		Allocation: allocation,
		IsNew:      true,
	}, startFailureInputAcceptable)
	if !errors.Is(err, errGenerationStartLeaseLost) {
		t.Fatalf("expected start lease loss, got %v", err)
	}
	runscID := serverRunscContainerID(t, ctx, st, session.ID, allocation.GenerationID)
	if got := rt.runtimeDestroyRequests(); len(got) != 1 || got[0] != runscID {
		t.Fatalf("owner loss should destroy started runtime %q, got %+v", runscID, got)
	}
	var status, ownerValue, errorClass, networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.lease_owner, ''), COALESCE(g.error_class, ''), n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&status, &ownerValue, &errorClass, &networkState, &resourceState); err != nil {
		t.Fatalf("query generation after owner loss: %v", err)
	}
	if status != "starting" ||
		ownerValue != "other_owner" ||
		errorClass != "" ||
		networkState != "allocating" ||
		resourceState != "allocating" {
		t.Fatalf("owner loss should not fail or reclaim the stolen generation: status=%s owner=%q class=%q network=%s resource=%s", status, ownerValue, errorClass, networkState, resourceState)
	}
	instance, err := st.GetRuntimeResourceInstance(ctx, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime resource instance after owner loss: %v", err)
	}
	if instance.State != store.RuntimeResourceRetiring {
		t.Fatalf("runtime resource after owner loss=%s want %s", instance.State, store.RuntimeResourceRetiring)
	}
	var runtimeEvents int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM events
WHERE session_id = ?
  AND type = 'generation.error'`, session.ID).Scan(&runtimeEvents); err != nil {
		t.Fatalf("count generation events: %v", err)
	}
	if runtimeEvents != 0 {
		t.Fatalf("owner loss should not publish generation error events, got %d", runtimeEvents)
	}
}

func TestRuntimeFailureClassDetectsPostStartProbeFailure(t *testing.T) {
	cases := []string{
		"harness-bridge-client probe exited with status 1",
		"bridge probe starting failed",
		"bridge startup probe did not complete: missing probe_network",
		"probe GET /healthz returned 503, want one of [200]",
		"probe POST /v1/messages returned 502, want one of [400]",
	}
	for _, message := range cases {
		if got := runtimeFailureClass(message); got != "probe_failed_post_start" {
			t.Fatalf("runtimeFailureClass(%q)=%s want probe_failed_post_start", message, got)
		}
	}
}

func TestRuntimeFailureClassDetectsManifestFailures(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"sandbox_secret_disallowed", "sandbox_secret_disallowed"},
		{"shell_secret_disallowed", "shell_secret_disallowed"},
		{"runsc run: exit status 1: control manifest digest mismatch", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected session_id=sess_a got sess_b", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected generation_id=gen_a got gen_b", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected network_profile_id=net_a got net_b", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected agent_runtime_profile_id=arp_a got arp_b", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected anthropic_api_key_secret_id=anthropic_api_key got other", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected anthropic_auth_token_secret_id=anthropic_auth_token got other", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected manifest_version=1 got 2", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: expected secret_version=local got rotated", "manifest_digest_mismatch"},
		{"runsc run: exit status 1: secret mount /harness-secrets missing", "manifest_digest_mismatch"},
	}
	for _, tc := range cases {
		if got := runtimeFailureClass(tc.message); got != tc.want {
			t.Fatalf("runtimeFailureClass(%q)=%s want %s", tc.message, got, tc.want)
		}
	}
}

func TestSendMessagePrepareFailureMarksGenerationFailedAndReclaimable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_prepare_fail", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	srv := &Server{
		cfg:   cfg,
		store: st,
		runtime: failingRuntime{
			prepareErr: errors.New("pre-start sandbox network probe failed"),
		},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"hello"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error_class"] != "probe_failed_pre_start" ||
		body["error"] != "sandbox network probe failed before start" {
		t.Fatalf("unexpected response body: %v", body)
	}
	var generationStatus, errorClass, networkState, resourceState, sessionStatus, sessionErrorClass, sessionFailureReason string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.error_class, ''), n.allocation_state, r.resource_state, s.status,
       COALESCE(s.error_class, ''), COALESCE(s.failure_reason, '')
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN sessions s ON s.active_generation_id = g.generation_id
WHERE g.session_id = ?`, session.ID).Scan(
		&generationStatus,
		&errorClass,
		&networkState,
		&resourceState,
		&sessionStatus,
		&sessionErrorClass,
		&sessionFailureReason,
	); err != nil {
		t.Fatalf("query generation state: %v", err)
	}
	if generationStatus != "failed" ||
		errorClass != "probe_failed_pre_start" ||
		networkState != "reclaimable" ||
		resourceState != "reclaimable" ||
		sessionStatus != string(sessionstate.Created) ||
		sessionErrorClass != "" ||
		sessionFailureReason != "" {
		t.Fatalf("unexpected failed generation state: generation=%s class=%s network=%s resource=%s session=%s session_class=%s session_reason=%s", generationStatus, errorClass, networkState, resourceState, sessionStatus, sessionErrorClass, sessionFailureReason)
	}
	if !sessionstate.CanAcceptInput(sessionStatus) {
		t.Fatalf("session should remain input-acceptable after prepare failure, got %s", sessionStatus)
	}
	var runtimeEvents int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM events
WHERE session_id = ?
  AND type = 'generation.error'`, session.ID).Scan(&runtimeEvents); err != nil {
		t.Fatalf("count generation error events: %v", err)
	}
	if runtimeEvents != 1 {
		t.Fatalf("expected one generation.error event, got %d", runtimeEvents)
	}
	var turns int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ?`, session.ID).Scan(&turns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turns != 0 {
		t.Fatalf("prepare failure should happen before turn creation, got %d turns", turns)
	}
}

func TestDestroySessionCancelsPendingTurnAndReclaimsGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_destroy_pending", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	enqueued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{
		SessionID: session.ID,
		Content:   "hello",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+session.ID, nil)
	rec := httptest.NewRecorder()
	srv.destroySession(rec, req, session.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %s", rec.Code, rec.Body.String())
	}
	destroyIDs := rt.runtimeDestroyRequests()
	runscID := serverRunscContainerID(t, ctx, st, session.ID, allocation.GenerationID)
	if len(destroyIDs) != 1 || destroyIDs[0] != runscID {
		t.Fatalf("destroy session should tear down runsc container id %q, got %+v", runscID, destroyIDs)
	}
	destroyGenerationRequests := rt.destroyGenerationRequests()
	if len(destroyGenerationRequests) != 1 || destroyGenerationRequests[0].GenerationID != allocation.GenerationID {
		t.Fatalf("destroy session should clean generation resources, got %+v", destroyGenerationRequests)
	}
	var sessionStatus, turnStatus, turnErrorClass, generationStatus, generationErrorClass, networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT s.status, t.status, COALESCE(t.error_class, ''), g.status, COALESCE(g.error_class, ''),
       n.allocation_state, r.resource_state
FROM sessions s
JOIN turns t ON t.session_id = s.id
JOIN runtime_generations g ON g.session_id = s.id
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE s.id = ?
  AND t.id = ?`, session.ID, enqueued.TurnID).Scan(
		&sessionStatus,
		&turnStatus,
		&turnErrorClass,
		&generationStatus,
		&generationErrorClass,
		&networkState,
		&resourceState,
	); err != nil {
		t.Fatalf("query destroyed state: %v", err)
	}
	if sessionStatus != string(sessionstate.Destroyed) ||
		turnStatus != "canceled" ||
		turnErrorClass != "session_destroyed" ||
		generationStatus != "failed" ||
		generationErrorClass != "session_destroyed" ||
		networkState != "destroyed" ||
		resourceState != "destroyed" {
		t.Fatalf("unexpected destroyed state: session=%s turn=%s turn_error=%s generation=%s generation_error=%s network=%s resource=%s",
			sessionStatus, turnStatus, turnErrorClass, generationStatus, generationErrorClass, networkState, resourceState)
	}
	var destroyedEvents int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM events
WHERE session_id = ?
  AND type = 'session.destroyed'`, session.ID).Scan(&destroyedEvents); err != nil {
		t.Fatalf("count destroyed events: %v", err)
	}
	if destroyedEvents != 1 {
		t.Fatalf("expected one durable destroyed event, got %d", destroyedEvents)
	}
}

func TestRenameSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_rename", string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{
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
	srv := &Server{
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
	srv := &Server{
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

func TestSendMessageSetsPlaceholderTitle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_placeholder", string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{
		cfg:   testServerConfig(dir),
		store: st,
		hub:   events.NewHub(),
		log:   slog.Default(),
	}

	// First message on an untitled session sets a non-custom placeholder title.
	srv.maybeSetPlaceholderTitle(ctx, session, "Help me refactor the auth module please")
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "Help me refactor the auth module please" || got.TitleCustom {
		t.Fatalf("placeholder title = %q custom = %v", got.Title, got.TitleCustom)
	}

	// A manual rename locks the title.
	if err := st.UpdateSessionTitle(ctx, session.ID, "Auth work", true); err != nil {
		t.Fatalf("manual rename: %v", err)
	}
	// A later first-message-style placeholder attempt must not overwrite it. Pass
	// the refreshed session (empty Title guard is on the passed-in value, so use a
	// copy that still looks untitled to prove the store-level guard holds).
	stale := session
	stale.Title = ""
	srv.maybeSetPlaceholderTitle(ctx, stale, "totally different first message")
	got, _ = st.GetSession(ctx, session.ID)
	if got.Title != "Auth work" || !got.TitleCustom {
		t.Fatalf("placeholder clobbered custom title: %q custom = %v", got.Title, got.TitleCustom)
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

func TestRunMaintenancePollsBridgeOutbox(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_bridge_poll", string(sessionstate.Created), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Bridge.PollInterval = config.Duration{Duration: 10 * time.Millisecond}
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config: store.ResourceAllocatorConfig{
			RunDir:             cfg.Harness.RunDir,
			CIDRPool:           cfg.Harness.Network.CIDRPool.Prefix,
			EgressDorisFEHosts: cfg.Harness.Network.Egress.DorisFEHosts,
			EgressDorisBEHosts: cfg.Harness.Network.Egress.DorisBEHosts,
			EgressDorisPorts:   cfg.Harness.Network.Egress.DorisPorts,
			EgressDNSPolicy:    string(cfg.Harness.Network.Egress.DNSPolicy),
			HostProxyBindURL:   cfg.ModelProxy.BindURL,
			ProxyPort:          cfg.ModelProxy.BindPort,
			Agent:              "pi",
			AgentModel:         "sonnet",
			AgentOutputFormat:  agents.PiEventSchemaVersion,
		},
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	createServerRuntimeResourceLive(t, ctx, st, session.ID, allocation, owner.UUID, "host-bridge-poll", time.Now().UTC())
	details, err := st.GetRuntimeGenerationDetails(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	outbox, err := bridge.OpenQueue(details.BridgeDirPath, bridge.OutboxDir)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	if _, err := outbox.Write(ctx, bridge.Envelope{
		MessageID:    "msg_hello",
		RequestID:    "req_hello",
		Type:         bridge.TypeHello,
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		Payload:      serverBridgeHelloPayload(t, session.Agent),
	}); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()

	response := waitForBridgeInboxResponse(t, runCtx, details.BridgeDirPath, bridge.TypeHelloAck, "req_hello")
	if response.GenerationID != allocation.GenerationID || response.SessionID != session.ID {
		t.Fatalf("unexpected bridge response identity: %+v", response)
	}
	if _, err := os.Stat(bridge.HeartbeatPath(details.BridgeDirPath, bridge.HostHeartbeatFile)); err != nil {
		t.Fatalf("host heartbeat file missing after bridge poll: %v", err)
	}
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance exit err=%v, want context canceled", err)
	}
}

func TestRunMaintenanceRecoversGenerationThatExpiresAfterStartup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_expiring_generation", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Bridge.HeartbeatInterval = config.Duration{Duration: 10 * time.Millisecond}
	cfg.Harness.Bridge.ReconnectGrace = config.Duration{Duration: 20 * time.Millisecond}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	expiresAt := time.Now().UTC().Add(25 * time.Millisecond)
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = ?,
    lease_expires_at = ?,
    last_seen_at = ?
WHERE generation_id = ?`,
		store.GenerationLeaseOwner("previous-owner"),
		expiresAt.Format(time.RFC3339Nano),
		expiresAt.Add(-time.Minute).Format(time.RFC3339Nano),
		allocation.GenerationID,
	); err != nil {
		t.Fatalf("move generation to previous owner: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()

	waitForGenerationStatus(t, runCtx, st, allocation.GenerationID, "failed")
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance exit err=%v, want context canceled", err)
	}

	var errorClass, leaseOwnerAfter string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COALESCE(error_class, ''), COALESCE(lease_owner, '')
FROM runtime_generations
WHERE generation_id = ?`, allocation.GenerationID).Scan(&errorClass, &leaseOwnerAfter); err != nil {
		t.Fatalf("query recovered generation: %v", err)
	}
	if errorClass != "orchestrator_restart_reconnect_grace_expired" || leaseOwnerAfter != "" {
		t.Fatalf("unexpected recovered generation: error_class=%s lease_owner=%s", errorClass, leaseOwnerAfter)
	}
	runscID := serverRunscContainerID(t, ctx, st, session.ID, allocation.GenerationID)
	if got := rt.runtimeDestroyRequests(); len(got) != 1 || got[0] != runscID {
		t.Fatalf("maintenance should destroy runtime before repair using runsc container id %q, got %+v", runscID, got)
	}
	if _, starts := rt.requests(); len(starts) != 0 {
		t.Fatalf("maintenance should not cold-start without a queued turn: %+v", starts)
	}
}

// TestRunMaintenanceRecoversSandboxStalledGeneration is the end-to-end
// regression test for the zombie-session bug. The generation is owned by the
// CURRENT maintenance owner with a still-valid lease — exactly the state a
// zombie is in, because before this fix RunMaintenance self-renewed the lease
// every tick and recovery could never fire. With the sandbox-liveness gate, a
// stale sandbox_last_seen_at stops the self-renewal, the lease lapses, and the
// reconnect-grace recovery branch fails the generation and frees the session.
func TestRunMaintenanceRecoversSandboxStalledGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_sandbox_stalled", string(sessionstate.RunningIdle), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	cfg.Harness.Bridge.HeartbeatInterval = config.Duration{Duration: 10 * time.Millisecond}
	cfg.Harness.Bridge.ReconnectGrace = config.Duration{Duration: 20 * time.Millisecond}
	cfg.Harness.Bridge.LeaseTTL = config.Duration{Duration: time.Minute}
	cfg.Harness.Bridge.SandboxHeartbeatStallGrace = config.Duration{Duration: 30 * time.Millisecond}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	// Current owner, lease still valid, but the sandbox has been silent for a
	// minute (stale sandbox_last_seen_at). This is the zombie state.
	now := time.Now().UTC()
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = ?,
    lease_expires_at = ?,
    sandbox_last_seen_at = ?
WHERE generation_id = ?`,
		leaseOwner,
		now.Add(15*time.Millisecond).Format(time.RFC3339Nano),
		now.Add(-time.Minute).Format(time.RFC3339Nano),
		allocation.GenerationID,
	); err != nil {
		t.Fatalf("set zombie lease state: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt := &recordingRuntime{}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()

	waitForGenerationStatus(t, runCtx, st, allocation.GenerationID, "failed")
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance exit err=%v, want context canceled", err)
	}

	var errorClass, leaseOwnerAfter, sessionStatus string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COALESCE(g.error_class, ''), COALESCE(g.lease_owner, ''), s.status
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&errorClass, &leaseOwnerAfter, &sessionStatus); err != nil {
		t.Fatalf("query recovered generation: %v", err)
	}
	// The orchestrator never restarted here — the sandbox stopped heartbeating
	// under the same owner — so the recorded cause must say so, not blame a
	// restart that did not happen.
	if errorClass != store.SandboxHeartbeatLostCause || leaseOwnerAfter != "" {
		t.Fatalf("unexpected recovered generation: error_class=%s lease_owner=%s", errorClass, leaseOwnerAfter)
	}
	// Session must return to running_idle so the frontend unblocks.
	if sessionStatus != string(sessionstate.RunningIdle) {
		t.Fatalf("expected session to return to running_idle, got %s", sessionStatus)
	}
}

// Cleanup failure alone no longer blocks the repair. It used to: recovery
// skipped the DB repair whenever `runsc delete` errored, so a sandbox whose
// control socket had wedged kept its generation in a non-terminal status forever,
// the one-nonterminal-per-session index blocked any replacement, and the
// session's turn stayed running indefinitely (22h / 1343 failed retries in the
// 2026-08-19 incident). When no host processes survive, the sandbox cannot touch
// its overlay again and the row is safe to release.
func TestExpiredRuntimeRecoveryRepairsWhenCleanupFailsButHostIsClean(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	session := createServerTestSession(t, ctx, st, dir, "sess_recovery_cleanup_fail", string(sessionstate.RunningIdle), now.Add(-2*time.Minute), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-3 * time.Minute),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    lease_owner = ?,
    lease_expires_at = ?
WHERE generation_id = ?`, store.GenerationLeaseOwner("previous-owner"), now.Add(-time.Minute).Format(time.RFC3339Nano), allocation.GenerationID); err != nil {
		t.Fatalf("expire generation: %v", err)
	}
	// Teardown fails, but nothing is left running on the host.
	rt := &recordingRuntime{destroyRuntimeErr: errors.New("runsc delete failed")}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	recovered, err := srv.RecoverExpiredRuntimeResources(ctx, now)
	if err != nil {
		t.Fatalf("recover expired runtime resources: %v", err)
	}
	if recovered.RuntimeCleanupSkipped != 0 {
		t.Fatalf("clean host should not count as skipped, got %+v", recovered)
	}
	if recovered.ReconnectGraceFailed != 1 {
		t.Fatalf("expected the generation to be released, got %+v", recovered)
	}
	runscID := serverRunscContainerID(t, ctx, st, session.ID, allocation.GenerationID)
	if got := rt.runtimeDestroyRequests(); len(got) != 1 || got[0] != runscID {
		t.Fatalf("expected runtime cleanup attempt for %q, got %+v", runscID, got)
	}
	var generationStatus, ownerAfter string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT status, COALESCE(lease_owner, '')
FROM runtime_generations
WHERE generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &ownerAfter); err != nil {
		t.Fatalf("query recovered generation: %v", err)
	}
	if generationStatus != "failed" || ownerAfter != "" {
		t.Fatalf("generation should be released for cold fallback: status=%s owner=%s", generationStatus, ownerAfter)
	}
}

// The safety gate: when host processes survive teardown the generation must stay
// locked. A replacement generation would mount the same overlay upperdir, and
// nothing else enforces single-attach, so releasing here risks corrupting the
// user's workspace. Availability for this one session yields to data integrity.
func TestExpiredRuntimeRecoveryHoldsGenerationWhenRemnantsSurvive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	session := createServerTestSession(t, ctx, st, dir, "sess_recovery_remnants", string(sessionstate.RunningIdle), now.Add(-2*time.Minute), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-3 * time.Minute),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    lease_owner = ?,
    lease_expires_at = ?
WHERE generation_id = ?`, store.GenerationLeaseOwner("previous-owner"), now.Add(-time.Minute).Format(time.RFC3339Nano), allocation.GenerationID); err != nil {
		t.Fatalf("expire generation: %v", err)
	}
	rt := &survivingRemnantRuntime{recordingRuntime: recordingRuntime{destroyRuntimeErr: errors.New("runsc delete failed")}}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	recovered, err := srv.RecoverExpiredRuntimeResources(ctx, now)
	if err != nil {
		t.Fatalf("recover expired runtime resources: %v", err)
	}
	if recovered.RuntimeCleanupSkipped != 1 ||
		recovered.ReconnectGraceFailed != 0 ||
		recovered.ExpiredLifecycleFailed != 0 ||
		recovered.UnknownAfterAckStarted != 0 {
		t.Fatalf("surviving remnants should hold the repair, got %+v", recovered)
	}
	var generationStatus, ownerAfter, networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT g.status, COALESCE(g.lease_owner, ''), n.allocation_state, r.resource_state
FROM runtime_generations g
JOIN network_profiles n ON n.generation_id = g.generation_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.generation_id = ?`, allocation.GenerationID).Scan(&generationStatus, &ownerAfter, &networkState, &resourceState); err != nil {
		t.Fatalf("query held recovery state: %v", err)
	}
	if generationStatus != "idle" ||
		ownerAfter != string(store.GenerationLeaseOwner("previous-owner")) ||
		networkState != "live" ||
		resourceState != "live" {
		t.Fatalf("surviving remnants should leave DB non-reclaimable: generation=%s owner=%s network=%s resource=%s", generationStatus, ownerAfter, networkState, resourceState)
	}
}

// survivingRemnantRuntime models the unreclaimable case: teardown failed and host
// processes are still alive.
type survivingRemnantRuntime struct {
	recordingRuntime
}

func (r *survivingRemnantRuntime) SandboxRemnantsGone(context.Context, string) (bool, error) {
	return false, nil
}

func TestDestroyReclaimableGenerationResourcesMarksDestroyedOnlyAfterRuntimeCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	for _, tc := range []struct {
		name       string
		destroyErr error
		wantState  string
	}{
		{name: "cleanup succeeds", wantState: "destroyed"},
		{name: "cleanup fails", destroyErr: errors.New("netns busy"), wantState: "reclaimable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, owner := openServerOwnedStore(t, ctx, dir)
			cfg := testServerConfig(dir)
			createServerTestSession(t, ctx, st, dir, "sess_cleanup", string(sessionstate.Created), now.Add(-time.Minute), nil)
			allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
				SessionID: "sess_cleanup",
				Owner:     store.GenerationLeaseOwner(owner.UUID),
				LeaseTTL:  time.Minute,
				Now:       now.Add(-time.Minute),
				Config:    serverTestAllocatorConfig(cfg, "pi"),
			})
			if err != nil {
				t.Fatalf("allocate generation: %v", err)
			}
			if err := st.MarkGenerationResourcesLive(ctx, "sess_cleanup", allocation.GenerationID, allocation.Owner, now.Add(-59*time.Second)); err != nil {
				t.Fatalf("mark resources live: %v", err)
			}
			if err := st.FailGeneration(ctx, store.FailGenerationParams{
				SessionID:    "sess_cleanup",
				GenerationID: allocation.GenerationID,
				Owner:        allocation.Owner,
				ErrorClass:   "probe_failed_pre_start",
				Reason:       "probe failed",
				Now:          now.Add(-58 * time.Second),
			}); err != nil {
				t.Fatalf("fail generation: %v", err)
			}

			rt := &recordingRuntime{destroyErr: tc.destroyErr}
			srv := &Server{
				cfg:     cfg,
				store:   st,
				runtime: rt,
				watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
				hub:     events.NewHub(),
				log:     slog.Default(),
			}
			srv.destroyReclaimableGenerationResources(ctx, now)

			calls := rt.destroyGenerationRequests()
			if len(calls) != 1 || calls[0].GenerationID != allocation.GenerationID {
				t.Fatalf("destroy generation calls=%+v", calls)
			}
			var networkState, resourceState string
			if err := st.DBForTest().QueryRowContext(ctx, `
SELECT n.allocation_state, r.resource_state
FROM network_profiles n
JOIN runtime_generation_resources r ON r.generation_id = n.generation_id
WHERE n.generation_id = ?`, allocation.GenerationID).Scan(&networkState, &resourceState); err != nil {
				t.Fatalf("query resource states: %v", err)
			}
			if networkState != tc.wantState || resourceState != tc.wantState {
				t.Fatalf("unexpected states after cleanup: network=%s resource=%s want %s", networkState, resourceState, tc.wantState)
			}
		})
	}
}

func TestDestroyReclaimableGenerationResourcesRemovesFilesystemWithRealRuntime(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	createServerTestSession(t, ctx, st, dir, "sess_cleanup_real", string(sessionstate.Created), now.Add(-time.Minute), nil)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: "sess_cleanup_real",
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-time.Minute),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, "sess_cleanup_real", allocation.GenerationID, allocation.Owner, now.Add(-59*time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	if err := st.FailGeneration(ctx, store.FailGenerationParams{
		SessionID:    "sess_cleanup_real",
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		ErrorClass:   "probe_failed_pre_start",
		Reason:       "probe failed",
		Now:          now.Add(-58 * time.Second),
	}); err != nil {
		t.Fatalf("fail generation: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, "sess_cleanup_real", allocation.GenerationID)
	if err != nil {
		t.Fatalf("get runtime generation details: %v", err)
	}
	createServerGenerationFilesystem(t, details)

	realRuntime := runtime.New(runtime.Config{
		RunscNetwork:    "sandbox",
		RunscOverlay2:   "none",
		RunscRoot:       filepath.Join(dir, "runsc-root"),
		RunDir:          cfg.Harness.RunDir,
		CheckpointsRoot: filepath.Join(dir, "checkpoints"),
		CommandRunner: serverCommandRunner{fail: map[string]error{
			"runsc -root " + filepath.Join(dir, "runsc-root") + " state " + details.RunscContainerID: errors.New("not found"),
			"ip link show " + details.HostVeth:                                         errors.New("does not exist"),
			"nft list table inet " + runtimeResourceNftTableName(details.GenerationID): errors.New("No such table"),
		}},
	})
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: realRuntime,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.destroyReclaimableGenerationResources(ctx, now)

	for _, path := range []string{details.CheckpointPath, details.ControlDirPath, details.BundleDirPath, details.BridgeDirPath, details.LogDirPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("expected cleanup path %s to be removed, stat err=%v", path, err)
		}
	}
	var networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT n.allocation_state, r.resource_state
FROM network_profiles n
JOIN runtime_generation_resources r ON r.generation_id = n.generation_id
WHERE n.generation_id = ?`, allocation.GenerationID).Scan(&networkState, &resourceState); err != nil {
		t.Fatalf("query resource states: %v", err)
	}
	if networkState != "destroyed" || resourceState != "destroyed" {
		t.Fatalf("unexpected states after real runtime cleanup: network=%s resource=%s", networkState, resourceState)
	}
}

func TestRunMaintenancePublishesBridgeOutputAndCompletion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_bridge_events", string(sessionstate.RunningActive), time.Now().UTC(), nil)
	if _, err := st.DBForTest().ExecContext(ctx, `UPDATE sessions SET driver_id = 'sh', mode = 'shell' WHERE id = ?`, session.ID); err != nil {
		t.Fatalf("set shell agent: %v", err)
	}
	session.Agent = "sh"
	session.Mode = "shell"
	cfg := testServerConfig(dir)
	cfg.Harness.Bridge.PollInterval = config.Duration{Duration: 10 * time.Millisecond}
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "sh"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	createServerRuntimeResourceLive(t, ctx, st, session.ID, allocation, owner.UUID, "host-bridge-events", time.Now().UTC())
	details, err := st.GetRuntimeGenerationDetails(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	turnID, err := st.EnqueueTurn(ctx, session.ID, "run", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "req_claim",
		LeaseTTL:     time.Minute,
		Now:          time.Now().UTC(),
	})
	if err != nil || !ok || grant.TurnID != turnID {
		t.Fatalf("claim setup: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	sandboxSourceIP := serverSandboxSourceIPForGeneration(t, ctx, st, allocation.GenerationID)
	outbox, err := bridge.OpenQueue(details.BridgeDirPath, bridge.OutboxDir)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	if _, err := outbox.Write(ctx, bridge.Envelope{
		MessageID:    "msg_started",
		Type:         bridge.TypeAckTurnStarted,
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		TurnID:       &turnID,
		Payload:      json.RawMessage(fmt.Sprintf(`{"sandbox_source_ip":%q}`, sandboxSourceIP)),
	}); err != nil {
		t.Fatalf("write started: %v", err)
	}
	if _, err := outbox.Write(ctx, bridge.Envelope{
		MessageID:    "msg_output",
		Type:         bridge.TypeEmitOutput,
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		TurnID:       &turnID,
		Payload:      json.RawMessage(`{"output_sequence":1,"stream":"stdout","payload":{"line":"{\"type\":\"harness.shell_output\",\"stream\":\"stdout\",\"text\":\"ok\\n\"}"}}`),
	}); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if _, err := outbox.Write(ctx, bridge.Envelope{
		MessageID:    "msg_done",
		Type:         bridge.TypeAckTurnCompleted,
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		TurnID:       &turnID,
		Payload:      json.RawMessage(`{"status":"completed"}`),
	}); err != nil {
		t.Fatalf("write done: %v", err)
	}

	hub := events.NewHub()
	eventsCh, cancelEvents := hub.Subscribe(session.ID)
	defer cancelEvents()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, hub),
		hub:     hub,
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()
	waitForSessionStatus(t, runCtx, st, session.ID, string(sessionstate.RunningIdle))
	waitForHubEvent(t, eventsCh, bridge.TypeAckTurnCompleted)
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance exit err=%v, want context canceled", err)
	}

	var assistantMessages int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM messages
WHERE session_id = ?
  AND role = 'assistant'
  AND content = 'ok
'`, session.ID).Scan(&assistantMessages); err != nil {
		t.Fatalf("assistant messages: %v", err)
	}
	if assistantMessages != 1 {
		t.Fatalf("assistant messages=%d want 1", assistantMessages)
	}
}

func TestBridgeFailedCompletionDoesNotFailSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_bridge_failed", string(sessionstate.RunningActive), time.Now().UTC(), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       time.Now().UTC(),
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	createServerRuntimeResourceLive(t, ctx, st, session.ID, allocation, owner.UUID, "host-bridge-failed", time.Now().UTC())
	turnID, err := st.EnqueueTurn(ctx, session.ID, "run", time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "req_failed",
		LeaseTTL:     time.Minute,
		Now:          time.Now().UTC(),
	})
	if err != nil || !ok || grant.TurnID != turnID {
		t.Fatalf("claim setup: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	if _, err := st.AckTurnStarted(ctx, store.AckStartedParams{
		SessionID:       session.ID,
		GenerationID:    allocation.GenerationID,
		TurnID:          turnID,
		Owner:           allocation.Owner,
		SandboxSourceIP: serverSandboxSourceIPForGeneration(t, ctx, st, allocation.GenerationID),
		LeaseTTL:        time.Minute,
		Now:             time.Now().UTC(),
	}); err != nil {
		t.Fatalf("ack started: %v", err)
	}
	completionPayload := map[string]string{
		"status":      "failed",
		"error_class": "agent_error",
		"error":       "agent exited 1",
	}
	eventID, err := st.CompleteTurn(ctx, store.CompleteTurnParams{
		SessionID:      session.ID,
		GenerationID:   allocation.GenerationID,
		TurnID:         turnID,
		Owner:          allocation.Owner,
		TerminalStatus: "failed",
		ErrorClass:     "agent_error",
		Error:          "agent exited 1",
		EventType:      bridge.TypeAckTurnCompleted,
		EventDedupeKey: "ack_completed:" + allocation.GenerationID,
		EventPayload:   completionPayload,
		Now:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("complete failed turn: %v", err)
	}

	hub := events.NewHub()
	eventsCh, cancelEvents := hub.Subscribe(session.ID)
	defer cancelEvents()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, hub),
		hub:     hub,
		log:     slog.Default(),
	}
	envelopePayload, err := json.Marshal(completionPayload)
	if err != nil {
		t.Fatalf("marshal completion payload: %v", err)
	}
	srv.handleBridgeCommittedEnvelope(ctx, bridge.Envelope{
		Type:         bridge.TypeAckTurnCompleted,
		SessionID:    session.ID,
		GenerationID: allocation.GenerationID,
		TurnID:       &turnID,
		Payload:      envelopePayload,
	}, eventID)

	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != string(sessionstate.RunningIdle) || !sessionstate.CanAcceptInput(got.Status) {
		t.Fatalf("failed completion should leave session retryable, got %s", got.Status)
	}
	seenCompletion := false
	for {
		select {
		case event := <-eventsCh:
			switch event.Type {
			case bridge.TypeAckTurnCompleted:
				seenCompletion = true
			case "session." + string(sessionstate.Failed), "session.error":
				t.Fatalf("unexpected terminal event after failed completion: %+v", event)
			}
		default:
			if !seenCompletion {
				t.Fatalf("missing durable completion event")
			}
			return
		}
	}
}

func TestRunMaintenancePrunesRetainedEvents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	createServerTestSession(t, ctx, st, dir, "sess_events_a", string(sessionstate.Created), now, nil)
	createServerTestSession(t, ctx, st, dir, "sess_events_b", string(sessionstate.Created), now, nil)
	firstID, err := st.AppendEvent(ctx, store.AppendEventParams{
		SessionID: "sess_events_a",
		Type:      "test.event",
		Payload:   map[string]string{"name": "first"},
		Now:       now.Add(-3 * time.Second),
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	secondID, err := st.AppendEvent(ctx, store.AppendEventParams{
		SessionID: "sess_events_b",
		Type:      "test.event",
		Payload:   map[string]string{"name": "second"},
		Now:       now.Add(-2 * time.Second),
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	thirdID, err := st.AppendEvent(ctx, store.AppendEventParams{
		SessionID: "sess_events_a",
		Type:      "test.event",
		Payload:   map[string]string{"name": "third"},
		Now:       now.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("append third event: %v", err)
	}

	cfg := testServerConfig(dir)
	cfg.Harness.Events.RetentionWindow = config.Duration{Duration: time.Hour}
	cfg.Harness.Events.RetentionRows = 2
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()
	waitForEventIDs(t, runCtx, st, []int64{secondID, thirdID})
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance exit err=%v, want context canceled", err)
	}
	if _, ok, err := st.GetEvent(ctx, firstID); err != nil || ok {
		t.Fatalf("first event retained ok=%v err=%v", ok, err)
	}
}

func TestSendMessageRejectsExpiredSessionBeforeAllocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	expired := time.Now().UTC().Add(-time.Second)
	session := createServerTestSession(t, ctx, st, dir, "sess_expired", string(sessionstate.Created), time.Now().UTC(), &expired)
	srv := &Server{
		cfg:     testServerConfig(dir),
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"hello"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d body %s", rec.Code, rec.Body.String())
	}
	var generations int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_generations`).Scan(&generations); err != nil {
		t.Fatalf("count generations: %v", err)
	}
	if generations != 0 {
		t.Fatalf("expired session should not allocate generation, got %d", generations)
	}
}

func TestProxyCorrelationUnixSocketPublishesDurableEvents(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "hp-proxy-")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	now := time.Now().UTC()
	allocation, turnID, sandboxSourceIP := createServerRunningProxyTurn(t, ctx, st, cfg, owner.UUID, dir, "sess_proxy_http", now)

	hub := events.NewHub()
	eventsCh, cancelEvents := hub.Subscribe("sess_proxy_http")
	defer cancelEvents()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, hub),
		hub:     hub,
		log:     slog.Default(),
	}

	publicReq := httptest.NewRequest(http.MethodPost, "/internal/proxy/requests/start", strings.NewReader(fmt.Sprintf(`{"sandbox_source_ip":%q,"proxy_request_id":"proxy_public"}`, sandboxSourceIP)))
	publicRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(publicRec, publicReq)
	if publicRec.Code != http.StatusNotFound {
		t.Fatalf("public proxy route status=%d body=%s", publicRec.Code, publicRec.Body.String())
	}

	directReq := httptest.NewRequest(http.MethodPost, "/internal/proxy/requests/start", strings.NewReader(fmt.Sprintf(`{"sandbox_source_ip":%q,"proxy_request_id":"proxy_direct"}`, sandboxSourceIP)))
	directRec := httptest.NewRecorder()
	srv.ProxyCorrelationRoutes().ServeHTTP(directRec, directReq)
	if directRec.Code != http.StatusForbidden {
		t.Fatalf("proxy route without peer credentials status=%d body=%s", directRec.Code, directRec.Body.String())
	}

	listener, socketPath, err := srv.ListenProxyCorrelation()
	if err != nil {
		t.Fatalf("listen proxy correlation: %v", err)
	}
	assertProxyCorrelationSocketPermissions(t, socketPath, cfg.Harness.ProxyServiceIdentity.GID)
	proxyServer := srv.ProxyCorrelationServer()
	errCh := make(chan error, 1)
	go func() { errCh <- proxyServer.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := proxyServer.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown proxy server: %v", err)
		}
		_ = os.Remove(socketPath)
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("proxy server stopped: %v", err)
		}
	})

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}}
	clientPost := func(path, body string) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://proxy.internal"+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build proxy request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("proxy request %s: %v", path, err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read proxy response: %v", err)
		}
		return resp.StatusCode, data
	}

	startStatus, startBody := clientPost("/internal/proxy/requests/start", fmt.Sprintf(`{
		"sandbox_source_ip":%q,
		"proxy_request_id":"proxy_http_1",
		"upstream_model":"claude-sonnet",
		"upstream_base_url":"https://api.anthropic.test"
	}`, sandboxSourceIP))
	if startStatus != http.StatusOK {
		t.Fatalf("start status=%d body=%s", startStatus, string(startBody))
	}
	var startResp struct {
		SessionID       string `json:"session_id"`
		TurnID          int64  `json:"turn_id"`
		GenerationID    string `json:"generation_id"`
		RequestSequence int64  `json:"request_sequence"`
		EventID         int64  `json:"event_id"`
		Replayed        bool   `json:"replayed"`
	}
	if err := json.Unmarshal(startBody, &startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if startResp.SessionID != "sess_proxy_http" || startResp.GenerationID != allocation.GenerationID ||
		startResp.TurnID != turnID || startResp.RequestSequence != 1 || startResp.EventID == 0 || startResp.Replayed {
		t.Fatalf("unexpected start response: %+v allocation=%+v turn=%d", startResp, allocation, turnID)
	}
	startEvent := waitForHubEvent(t, eventsCh, "proxy.request.started")
	if startEvent.EventID != startResp.EventID || startEvent.ProxyRequestID != "proxy_http_1" ||
		startEvent.SessionID != "sess_proxy_http" {
		t.Fatalf("unexpected start hub event: %+v response=%+v", startEvent, startResp)
	}

	finishStatus, finishBody := clientPost("/internal/proxy/requests/finish", `{
		"proxy_request_id":"proxy_http_1",
		"http_status":200,
		"upstream_total_latency_ms":321,
		"retry_count":0
	}`)
	if finishStatus != http.StatusOK {
		t.Fatalf("finish status=%d body=%s", finishStatus, string(finishBody))
	}
	var finishResp struct {
		Status       string `json:"status"`
		EventID      int64  `json:"event_id"`
		EventType    string `json:"event_type"`
		SessionID    string `json:"session_id"`
		TurnID       int64  `json:"turn_id"`
		GenerationID string `json:"generation_id"`
		Replayed     bool   `json:"replayed"`
	}
	if err := json.Unmarshal(finishBody, &finishResp); err != nil {
		t.Fatalf("decode finish response: %v", err)
	}
	if finishResp.Status != "accepted" || finishResp.EventType != "proxy.request.completed" ||
		finishResp.SessionID != "sess_proxy_http" || finishResp.GenerationID != allocation.GenerationID ||
		finishResp.TurnID != turnID || finishResp.EventID <= startResp.EventID || finishResp.Replayed {
		t.Fatalf("unexpected finish response: %+v start=%+v", finishResp, startResp)
	}
	finishEvent := waitForHubEvent(t, eventsCh, "proxy.request.completed")
	if finishEvent.EventID != finishResp.EventID || finishEvent.ProxyRequestID != "proxy_http_1" {
		t.Fatalf("unexpected finish hub event: %+v response=%+v", finishEvent, finishResp)
	}

	unknownStatus, unknownBody := clientPost("/internal/proxy/requests/finish", `{"proxy_request_id":"proxy_missing"}`)
	if unknownStatus != http.StatusOK {
		t.Fatalf("unknown finish status=%d body=%s", unknownStatus, string(unknownBody))
	}
	var unknownResp map[string]string
	if err := json.Unmarshal(unknownBody, &unknownResp); err != nil {
		t.Fatalf("decode unknown finish response: %v", err)
	}
	if unknownResp["status"] != "stale_unknown_request" {
		t.Fatalf("unexpected unknown finish response: %v", unknownResp)
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

	srv := &Server{store: st, hub: events.NewHub(), log: slog.Default()}
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

func waitForBridgeInboxResponse(t *testing.T, ctx context.Context, root, responseType, requestID string) bridge.Envelope {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		queue, err := bridge.OpenQueue(root, bridge.InboxDir)
		if err != nil {
			t.Fatalf("open inbox: %v", err)
		}
		files, err := queue.ReadAll()
		if err != nil {
			t.Fatalf("read inbox: %v", err)
		}
		for _, file := range files {
			if file.Envelope.Type == responseType && file.Envelope.RequestID == requestID {
				if err := file.Unlink(); err != nil {
					t.Fatalf("unlink response: %v", err)
				}
				return file.Envelope
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled before bridge response")
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for bridge response %s/%s", responseType, requestID)
	return bridge.Envelope{}
}

func TestDownloadArtifactAllowsNestedRegularFile(t *testing.T) {
	srv, _, workspace := newArtifactDownloadServer(t, "sess_1")
	sessionDir := filepath.Join(workspace, "reports")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/reports/summary.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestDownloadArtifactPDFAllowsSameOriginFraming(t *testing.T) {
	srv, _, workspace := newArtifactDownloadServer(t, "sess_1")
	if err := os.WriteFile(filepath.Join(workspace, "report.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/report.pdf", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %s", rec.Code, rec.Body.String())
	}
	// The workbench previews PDFs in an iframe; DENY/sandbox would make the browser
	// refuse to frame the file ("<host> refused to connect"). X-Frame-Options must
	// be absent rather than SAMEORIGIN: the app is embeddable in third-party pages,
	// and a legacy header disagreeing with frame-ancestors * blocks the frame.
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("expected no X-Frame-Options, got %q", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "sandbox") {
		t.Fatalf("PDF CSP must not use sandbox, got %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors *") {
		t.Fatalf("expected frame-ancestors * in PDF CSP, got %q", csp)
	}
}

func TestDownloadArtifactNonFramableTypeStaysInert(t *testing.T) {
	srv, _, workspace := newArtifactDownloadServer(t, "sess_1")
	if err := os.WriteFile(filepath.Join(workspace, "summary.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/summary.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Fatalf("unexpected CSP for inert type: %q", got)
	}
}

func TestDownloadArtifactRejectsMissingWorkspaceEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testArtifactDownloadConfig(t, dir)
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		t.Fatalf("mkdir db state: %v", err)
	}
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sessionDir := filepath.Join(cfg.SessionsRoot, "sess_1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "orphan.txt"), []byte("orphan"), 0o644); err != nil {
		t.Fatalf("write orphan artifact: %v", err)
	}

	srv := &Server{cfg: cfg, store: st}
	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/orphan.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDownloadArtifactRejectsInvalidWorkspaceEvidence(t *testing.T) {
	srv, st, workspace := newArtifactDownloadServer(t, "sess_1")
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("report"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(context.Background(), `
UPDATE session_workspaces
SET host_path = ?
WHERE session_id = ?`, filepath.Join(filepath.Dir(workspace), "other_session"), "sess_1"); err != nil {
		t.Fatalf("corrupt workspace evidence: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/report.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d body %s", rec.Code, rec.Body.String())
	}
}

func newArtifactDownloadServer(t *testing.T, sessionID string) (*Server, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testArtifactDownloadConfig(t, dir)
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		t.Fatalf("mkdir db state: %v", err)
	}
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	createServerTestSession(t, ctx, st, dir, sessionID, string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{cfg: cfg, store: st}
	volumeConfig, err := srv.dataVolumeProvisionerConfig()
	if err != nil {
		t.Fatalf("data volume config: %v", err)
	}
	workspace, err := (localworkspace.Volumes{Records: st}).ProvisionSessionWorkspace(ctx, localworkspace.ProvisionSessionWorkspaceParams{
		SessionID: sessionID,
		Config:    volumeConfig,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("provision workspace: %v", err)
	}
	return srv, st, workspace.HostPath
}

func testArtifactDownloadConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg := testServerConfig(dir)
	cfg.AgentHomesRoot = filepath.Join(dir, "agent-homes")
	cfg.CheckpointsRoot = filepath.Join(dir, "checkpoints")
	cfg.BundleRoot = filepath.Join(dir, "bundle")
	cfg.RootFSPath = filepath.Join(dir, "rootfs")
	cfg.DBPath = filepath.Join(dir, "state", "orchestrator.db")
	cfg.RepoRoot = dir
	return cfg
}

// cleanSandboxHost is the default host-inspection behaviour for the runtime
// fakes: no surviving host processes and no OOM evidence. Embedded rather than
// repeated so a fake only spells these out when the test is about them.
type cleanSandboxHost struct{}

func (cleanSandboxHost) SandboxRemnantsGone(context.Context, string) (bool, error) {
	return true, nil
}

func (cleanSandboxHost) SandboxOOMKilled(context.Context, string) (bool, bool) {
	return false, false
}

// No memcg reading in tests by default: memory-pressure warnings are opt-in per
// test, so an unrelated test never has to reason about them.
func (cleanSandboxHost) SandboxMemoryUsage(context.Context, string) (int64, int64, bool) {
	return 0, 0, false
}

type instantRuntime struct{ cleanSandboxHost }

var instantRuntimePrepareCalls int64

func (instantRuntime) PrepareGeneration(_ context.Context, req runtime.StartRequest) (runtime.GenerationArtifacts, error) {
	atomic.AddInt64(&instantRuntimePrepareCalls, 1)
	return testGenerationArtifactsForAgent(req.Agent), nil
}

func (instantRuntime) Start(ctx context.Context, req runtime.StartRequest, output func(runtime.Output)) runtime.Result {
	if output != nil {
		output(runtime.Output{Stream: "stdout", Line: `{"type":"result","subtype":"success","result":"ok"}`})
	}
	return serverRuntimeStartResult(req)
}

func (instantRuntime) Destroy(context.Context, string) error {
	return nil
}

func (instantRuntime) DestroyGenerationResources(_ context.Context, details store.RuntimeGenerationDetails) (runtime.GenerationResourceCleanup, error) {
	return runtimeCleanupEvidenceForDetails(details), nil
}

func (instantRuntime) Interrupt(string) error {
	return nil
}

type recordingRuntime struct {
	cleanSandboxHost
	mu                sync.Mutex
	prepareRequests   []runtime.StartRequest
	startRequests     []runtime.StartRequest
	destroyRuntimeIDs []string
	destroyRuntimeErr error
	destroyRequests   []store.RuntimeGenerationDetails
	destroyErr        error
}

type blockingPrepareRuntime struct {
	recordingRuntime
	prepareStarted chan struct{}
	releasePrepare chan struct{}
	startedOnce    sync.Once
	releaseOnce    sync.Once
}

func newBlockingPrepareRuntime() *blockingPrepareRuntime {
	return &blockingPrepareRuntime{
		prepareStarted: make(chan struct{}),
		releasePrepare: make(chan struct{}),
	}
}

func (r *blockingPrepareRuntime) PrepareGeneration(ctx context.Context, req runtime.StartRequest) (runtime.GenerationArtifacts, error) {
	r.mu.Lock()
	r.prepareRequests = append(r.prepareRequests, req)
	r.mu.Unlock()
	r.startedOnce.Do(func() { close(r.prepareStarted) })
	select {
	case <-r.releasePrepare:
		return testGenerationArtifactsForAgent(req.Agent), nil
	case <-ctx.Done():
		return runtime.GenerationArtifacts{}, ctx.Err()
	}
}

func (r *blockingPrepareRuntime) release() {
	r.releaseOnce.Do(func() { close(r.releasePrepare) })
}

type startHookRuntime struct {
	recordingRuntime
	onStart func(runtime.StartRequest)
}

func (r *startHookRuntime) Start(_ context.Context, req runtime.StartRequest, _ func(runtime.Output)) runtime.Result {
	r.mu.Lock()
	r.startRequests = append(r.startRequests, req)
	r.mu.Unlock()
	if r.onStart != nil {
		r.onStart(req)
	}
	return serverRuntimeStartResult(req)
}

type claimAfterProbeRuntime struct {
	recordingRuntime
}

func (r *claimAfterProbeRuntime) Start(_ context.Context, req runtime.StartRequest, _ func(runtime.Output)) runtime.Result {
	r.mu.Lock()
	r.startRequests = append(r.startRequests, req)
	r.mu.Unlock()
	result := serverRuntimeStartResult(req)
	if result.Err != nil {
		return result
	}
	outbox, err := bridge.OpenQueue(req.Generation.BridgeDirPath, bridge.OutboxDir)
	if err != nil {
		return runtime.Result{Err: err}
	}
	if _, err := outbox.Write(context.Background(), bridge.Envelope{
		RequestID:    "test_claim_after_probe",
		Type:         bridge.TypeClaimNextTurn,
		SessionID:    req.SessionID,
		GenerationID: req.GenerationID,
	}); err != nil {
		return runtime.Result{Err: err}
	}
	return result
}

func (r *recordingRuntime) PrepareGeneration(_ context.Context, req runtime.StartRequest) (runtime.GenerationArtifacts, error) {
	r.mu.Lock()
	r.prepareRequests = append(r.prepareRequests, req)
	r.mu.Unlock()
	return testGenerationArtifactsForAgent(req.Agent), nil
}

func (r *recordingRuntime) Start(_ context.Context, req runtime.StartRequest, _ func(runtime.Output)) runtime.Result {
	r.mu.Lock()
	r.startRequests = append(r.startRequests, req)
	r.mu.Unlock()
	return serverRuntimeStartResult(req)
}

func (r *recordingRuntime) DestroyGenerationResources(_ context.Context, details store.RuntimeGenerationDetails) (runtime.GenerationResourceCleanup, error) {
	r.mu.Lock()
	r.destroyRequests = append(r.destroyRequests, details)
	err := r.destroyErr
	r.mu.Unlock()
	if err != nil {
		return runtime.GenerationResourceCleanup{}, err
	}
	return runtimeCleanupEvidenceForDetails(details), nil
}

func runtimeCleanupEvidenceForDetails(details store.RuntimeGenerationDetails) runtime.GenerationResourceCleanup {
	filesystem := map[string]string{}
	addFilesystem := func(label, path string) {
		if strings.TrimSpace(path) != "" {
			filesystem[label+":"+path] = "lstat:absent"
		}
	}
	addFilesystem("checkpoint", details.CheckpointPath)
	addFilesystem("control", details.ControlDirPath)
	addFilesystem("control_manifest", details.ControlManifestPath)
	addFilesystem("bundle", details.BundleDirPath)
	addFilesystem("spec", details.SpecPath)
	addFilesystem("bridge", details.BridgeDirPath)
	if strings.TrimSpace(details.NetworkHostsPath) != "" {
		addFilesystem("network", filepath.Dir(details.NetworkHostsPath))
	}
	addFilesystem("network_hosts", details.NetworkHostsPath)
	addFilesystem("log", details.LogDirPath)
	if len(filesystem) == 0 {
		filesystem["test:runtime_resource"] = "lstat:absent"
	}
	return runtime.GenerationResourceCleanup{
		RunscDeleted:      true,
		CheckpointDeleted: true,
		ControlDirDeleted: true,
		BundleDirDeleted:  true,
		BridgeDirDeleted:  true,
		NetworkDirDeleted: true,
		LogDirDeleted:     true,
		NetnsDeleted:      true,
		HostVethDeleted:   true,
		NftTableDeleted:   true,
		RunscState:        "runsc_container:absent; check=test",
		IPNetns:           "netns:absent; check=test",
		IPLink:            "host_veth:absent; check=test",
		NFT:               "nft_table:absent; check=test",
		FilesystemLstat:   filesystem,
	}
}

func (r *recordingRuntime) Destroy(_ context.Context, runtimeID string) error {
	r.mu.Lock()
	r.destroyRuntimeIDs = append(r.destroyRuntimeIDs, runtimeID)
	err := r.destroyRuntimeErr
	r.mu.Unlock()
	return err
}

func (r *recordingRuntime) Interrupt(string) error {
	return nil
}

func (r *recordingRuntime) requests() ([]runtime.StartRequest, []runtime.StartRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prepares := append([]runtime.StartRequest(nil), r.prepareRequests...)
	starts := append([]runtime.StartRequest(nil), r.startRequests...)
	return prepares, starts
}

func (r *recordingRuntime) destroyGenerationRequests() []store.RuntimeGenerationDetails {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.RuntimeGenerationDetails(nil), r.destroyRequests...)
}

func (r *recordingRuntime) runtimeDestroyRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.destroyRuntimeIDs...)
}

type restoreFailoverRuntime struct {
	recordingRuntime
	err     error
	coldErr error
}

func (r *restoreFailoverRuntime) Start(_ context.Context, req runtime.StartRequest, _ func(runtime.Output)) runtime.Result {
	r.mu.Lock()
	r.startRequests = append(r.startRequests, req)
	r.mu.Unlock()
	if req.RestoreFromCheckpoint {
		return runtime.Result{Err: r.err}
	}
	if r.coldErr != nil {
		return runtime.Result{Err: r.coldErr}
	}
	return serverRuntimeStartResult(req)
}

type restoreStartHookRuntime struct {
	recordingRuntime
	onRestoreStart func()
}

func (r *restoreStartHookRuntime) Start(_ context.Context, req runtime.StartRequest, _ func(runtime.Output)) runtime.Result {
	r.mu.Lock()
	r.startRequests = append(r.startRequests, req)
	r.mu.Unlock()
	if req.RestoreFromCheckpoint && r.onRestoreStart != nil {
		r.onRestoreStart()
	}
	return serverRuntimeStartResult(req)
}

type restoreValidationRuntime struct {
	cleanSandboxHost
	restore       *runtime.Runtime
	startRequests []runtime.StartRequest
}

func (r *restoreValidationRuntime) PrepareGeneration(_ context.Context, req runtime.StartRequest) (runtime.GenerationArtifacts, error) {
	return testGenerationArtifactsForAgent(req.Agent), nil
}

func (r *restoreValidationRuntime) Start(ctx context.Context, req runtime.StartRequest, output func(runtime.Output)) runtime.Result {
	r.startRequests = append(r.startRequests, req)
	if req.RestoreFromCheckpoint {
		return r.restore.Start(ctx, req, output)
	}
	return serverRuntimeStartResult(req)
}

func (r *restoreValidationRuntime) Destroy(context.Context, string) error {
	return nil
}

func (r *restoreValidationRuntime) DestroyGenerationResources(context.Context, store.RuntimeGenerationDetails) (runtime.GenerationResourceCleanup, error) {
	return runtime.GenerationResourceCleanup{}, nil
}

func (r *restoreValidationRuntime) Interrupt(string) error {
	return nil
}

type serverCommandRunner struct {
	outputs map[string][]byte
	fail    map[string]error
}

func (r serverCommandRunner) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if err := r.fail[key]; err != nil {
		return nil, err
	}
	if out, ok := r.outputs[key]; ok {
		return out, nil
	}
	return nil, nil
}

type failingRuntime struct {
	cleanSandboxHost
	prepareErr error
	err        error
}

func (f failingRuntime) PrepareGeneration(context.Context, runtime.StartRequest) (runtime.GenerationArtifacts, error) {
	if f.prepareErr != nil {
		return runtime.GenerationArtifacts{}, f.prepareErr
	}
	return testGenerationArtifacts(), nil
}

func (f failingRuntime) Start(context.Context, runtime.StartRequest, func(runtime.Output)) runtime.Result {
	return runtime.Result{Err: f.err}
}

func (f failingRuntime) Destroy(context.Context, string) error {
	return nil
}

func (f failingRuntime) DestroyGenerationResources(_ context.Context, details store.RuntimeGenerationDetails) (runtime.GenerationResourceCleanup, error) {
	return runtimeCleanupEvidenceForDetails(details), nil
}

func (f failingRuntime) Interrupt(string) error {
	return nil
}

func testGenerationArtifacts() runtime.GenerationArtifacts {
	return testGenerationArtifactsForAgent(string(agents.Pi))
}

func testGenerationArtifactsForAgent(agent string) runtime.GenerationArtifacts {
	var materialized []runtime.DriverConfigMaterialization
	for _, spec := range agents.DriverConfigMaterializationSpecsFor(agents.ID(strings.TrimSpace(agent))) {
		materialized = append(materialized, runtime.DriverConfigMaterialization{
			Name:                        spec.Name,
			SourceProjectionPath:        spec.SourceProjectionPath,
			HostSourcePath:              spec.HostSourcePath("/tmp/control"),
			SourceDigest:                "sha256:driver-config-" + spec.Name,
			SandboxDestination:          spec.SandboxDestination,
			DestinationMutableBySandbox: spec.DestinationMutableBySandbox,
		})
	}
	return runtime.GenerationArtifacts{
		BundleDir:                "/tmp/bundle",
		SpecPath:                 "/tmp/bundle/config.json",
		ManifestPath:             "/tmp/control/session.json",
		ManifestDigest:           "manifest_digest",
		ProjectedManifestDigest:  "projected_manifest_digest",
		BundleDigest:             "bundle_digest",
		RuntimeConfigDigest:      "runtime_config_digest",
		SpecDigest:               "spec_digest",
		RunscVersion:             "runsc test",
		RunscBinaryPath:          "/usr/local/bin/runsc-test",
		RunscBinaryDigest:        "sha256:runsc-test",
		MaterializedDriverConfig: materialized,
	}
}

func serverRuntimeStartResult(req runtime.StartRequest) runtime.Result {
	if err := writeServerBridgeBootstrapForRequest(req); err != nil {
		return runtime.Result{Err: err}
	}
	return runtime.Result{
		ControlManifestDigest: req.PreparedArtifacts.ManifestDigest,
		RunscVersion:          req.PreparedArtifacts.RunscVersion,
		PostStartProof:        serverPostStartProofForRequest(req),
	}
}

func writeServerBridgeBootstrapForRequest(req runtime.StartRequest) error {
	if strings.TrimSpace(req.Generation.BridgeDirPath) == "" {
		return nil
	}
	if err := bridge.EnsureLayout(req.Generation.BridgeDirPath); err != nil {
		return err
	}
	if err := bridge.TouchHeartbeat(req.Generation.BridgeDirPath, bridge.BridgeHeartbeatFile, time.Now().UTC()); err != nil {
		return err
	}
	outbox, err := bridge.OpenQueue(req.Generation.BridgeDirPath, bridge.OutboxDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	helloPayload, err := json.Marshal(map[string]any{"driver_id": req.Agent, "protocol_version": 2, "turn_input_schema": "RunTurn"})
	if err != nil {
		return err
	}
	for _, envelope := range []bridge.Envelope{
		{
			RequestID:    "test_heartbeat",
			Type:         bridge.TypeHeartbeat,
			SessionID:    req.SessionID,
			GenerationID: req.GenerationID,
		},
		{
			RequestID:    "test_hello",
			Type:         bridge.TypeHello,
			SessionID:    req.SessionID,
			GenerationID: req.GenerationID,
			Payload:      helloPayload,
		},
		{
			RequestID:    "test_probe",
			Type:         bridge.TypeProbeNetwork,
			SessionID:    req.SessionID,
			GenerationID: req.GenerationID,
		},
	} {
		if _, err := outbox.Write(ctx, envelope); err != nil {
			return err
		}
	}
	return nil
}

func serverPostStartProofForRequest(req runtime.StartRequest) *store.RuntimeResourcePostStartProof {
	containerID := strings.TrimSpace(req.Generation.RunscContainerID)
	if containerID == "" {
		containerID = "harness-gen-" + req.GenerationID
	}
	runscPlatform := strings.TrimSpace(req.Generation.RunscPlatform)
	if runscPlatform == "" {
		runscPlatform = "systrap"
	}
	runscVersion := strings.TrimSpace(req.Generation.RunscVersion)
	if runscVersion == "" {
		runscVersion = req.PreparedArtifacts.RunscVersion
	}
	runscBinaryPath := strings.TrimSpace(req.Generation.RunscBinaryPath)
	if runscBinaryPath == "" {
		runscBinaryPath = req.PreparedArtifacts.RunscBinaryPath
	}
	runscBinaryDigest := strings.TrimSpace(req.Generation.RunscBinaryDigest)
	if runscBinaryDigest == "" {
		runscBinaryDigest = req.PreparedArtifacts.RunscBinaryDigest
	}
	return &store.RuntimeResourcePostStartProof{
		GenerationID:      req.Generation.GenerationID,
		RunscContainerID:  containerID,
		RunscState:        "runsc_container:" + containerID + ":running; check=test",
		RunscPlatform:     runscPlatform,
		RunscVersion:      runscVersion,
		RunscBinaryPath:   runscBinaryPath,
		RunscBinaryDigest: runscBinaryDigest,
		IPNetns:           "netns:present; check=test",
		IPLink:            "host_veth:present; check=test",
		NFT:               "nft_table:present; check=test",
	}
}

func serverBridgeHelloPayload(t *testing.T, driverID string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"driver_id":         driverID,
		"protocol_version":  2,
		"turn_input_schema": "RunTurn",
	})
	if err != nil {
		t.Fatalf("marshal bridge hello payload: %v", err)
	}
	return payload
}

func recordServerRuntimeArtifacts(t *testing.T, ctx context.Context, st *store.Store, generationID, manifestDigest, runscVersion string) {
	t.Helper()
	artifacts := testGenerationArtifacts()
	recordServerRuntimeArtifactsWithRunsc(t, ctx, st, generationID, manifestDigest, runscVersion, artifacts.RunscBinaryPath, artifacts.RunscBinaryDigest)
}

func recordServerRuntimeArtifactsWithRunsc(t *testing.T, ctx context.Context, st *store.Store, generationID, manifestDigest, runscVersion, runscPath, runscDigest string) {
	t.Helper()
	artifacts := testGenerationArtifacts()
	artifacts.ManifestDigest = manifestDigest
	artifacts.ProjectedManifestDigest = manifestDigest
	artifacts.RunscVersion = runscVersion
	artifacts.RunscBinaryPath = runscPath
	artifacts.RunscBinaryDigest = runscDigest
	if err := st.RecordGenerationRuntimeArtifactDigests(ctx, generationID, store.GenerationRuntimeArtifactDigests{
		ControlManifestDigest:          artifacts.ManifestDigest,
		ProjectedControlManifestDigest: artifacts.ProjectedManifestDigest,
		BundleDigest:                   artifacts.BundleDigest,
		RuntimeConfigDigest:            artifacts.RuntimeConfigDigest,
		SpecDigest:                     artifacts.SpecDigest,
		RunscVersion:                   artifacts.RunscVersion,
		RunscBinaryPath:                artifacts.RunscBinaryPath,
		RunscBinaryDigest:              artifacts.RunscBinaryDigest,
	}); err != nil {
		t.Fatalf("record runtime artifacts: %v", err)
	}
}

func currentRunscBinaryMetadataForServerTest() (string, string) {
	path, err := exec.LookPath("runsc")
	if err != nil {
		return "runsc", "unavailable:" + err.Error()
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonical = filepath.Clean(path)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return canonical, "unavailable:" + err.Error()
	}
	sum := sha256.Sum256(data)
	return canonical, fmt.Sprintf("sha256:%x", sum[:])
}

func writeServerTestAgentImageManifest(t *testing.T, rootfs string, drivers ...agents.ID) string {
	t.Helper()
	entries := make([]imageManifestDriver, 0, len(drivers))
	buildDrivers := make([]string, 0, len(drivers))
	for _, driverID := range drivers {
		spec, ok := agents.DriverSpecFor(string(driverID))
		if !ok {
			t.Fatalf("missing driver spec for %s", driverID)
		}
		binaryPath := expectedDriverBinaryPath(driverID)
		hostPath := filepath.Join(rootfs, strings.TrimPrefix(binaryPath, "/"))
		if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
			t.Fatalf("mkdir driver binary parent: %v", err)
		}
		content := []byte("test binary for " + string(driverID) + "\n")
		if err := os.WriteFile(hostPath, content, 0o755); err != nil {
			t.Fatalf("write driver binary: %v", err)
		}
		sum := sha256.Sum256(content)
		entry := syntheticManifestDriver(spec)
		entry.InstalledBinaryDigest = fmt.Sprintf("sha256:%x", sum[:])
		entries = append(entries, entry)
		buildDrivers = append(buildDrivers, string(driverID))
	}
	manifest := map[string]any{
		"schema_version": 1,
		"build_input": map[string]any{
			"sandbox_agent_drivers": buildDrivers,
		},
		"drivers": entries,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	data = append(data, '\n')
	manifestPath := filepath.Join(rootfs, "etc", "harness-image", "agents.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("mkdir manifest parent: %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return manifestPath
}

func createServerRuntimeResourceLive(t *testing.T, ctx context.Context, st *store.Store, sessionID string, allocation store.GenerationAllocation, ownerUUID, hostID string, now time.Time) store.RuntimeResourceInstance {
	t.Helper()
	contractID := sandboxContractID(allocation.GenerationID)
	details, err := st.GetRuntimeGenerationDetails(ctx, sessionID, allocation.GenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	prefix, err := netip.ParsePrefix(details.SandboxIPCIDR)
	if err != nil {
		t.Fatalf("parse sandbox cidr: %v", err)
	}
	credentialPolicy := serverCredentialPolicyForTest(t, allocation.DriverState.DriverID)
	modelAccessAllowed := allocation.DriverState.DriverID == "pi"
	runtimeMaterialized := map[string]any{}
	mountMaterialized := map[string]any{}
	for _, spec := range agents.DriverConfigMaterializationSpecsFor(agents.ID(allocation.DriverState.DriverID)) {
		runtimeMaterialized[spec.Name] = map[string]any{
			"source_projection_path":         spec.SourceProjectionPath,
			"source_digest":                  "sha256:driver-config-" + spec.Name,
			"sandbox_destination":            spec.SandboxDestination,
			"destination_mutable_by_sandbox": spec.DestinationMutableBySandbox,
		}
		mountMaterialized[spec.Name] = map[string]any{
			"type":                           spec.MountType,
			"mode":                           spec.MountMode,
			"exact":                          spec.MountExact,
			"source_projection_path":         spec.SourceProjectionPath,
			"sandbox_destination":            spec.SandboxDestination,
			"destination_mutable_by_sandbox": spec.DestinationMutableBySandbox,
		}
	}
	if _, err := st.StoreSandboxContract(ctx, store.StoreSandboxContractParams{
		ContractID:   contractID,
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		Payload: map[string]any{
			"sandbox_contract_version": store.SandboxContractVersion,
			"contract_schema_version":  store.SandboxContractSchemaVersion,
			"contract_gate_version":    store.SandboxContractGateDriverManifest,
			"contract_id":              contractID,
			"session_id":               sessionID,
			"generation_id":            allocation.GenerationID,
			"runtime_profile_id":       allocation.AgentRuntimeProfileID,
			"network_profile_id":       allocation.NetworkProfileID,
			"driver": map[string]any{
				"driver_id":                            allocation.DriverState.DriverID,
				"driver_version":                       "test",
				"bridge_protocol":                      "harness_bridge_v2",
				"bridge_protocol_version":              2,
				"turn_input_schema":                    "RunTurn",
				"output_schema":                        agents.PiEventSchemaVersion,
				"command_argv_digest":                  "sha256:command",
				"driver_config_digest":                 "sha256:driver-config",
				"required_runtime_capabilities_digest": "sha256:driver-capabilities",
				"supports_interrupt":                   false,
				"supports_compaction":                  true,
			},
			"runtime_provider": map[string]any{
				"provider_id":              "local_runsc",
				"provider_profile_id":      "local_runsc_default",
				"isolation_kind":           "gvisor",
				"template_ref":             "default",
				"template_digest":          "sha256:template",
				"capability_vocab_version": "1",
				"capability_digest":        "sha256:provider-capabilities",
			},
			"identity": map[string]any{
				"model_access_allowed": modelAccessAllowed,
			},
			"network_identity": map[string]any{
				"runsc_network": details.RunscNetwork,
				"sandbox_ip":    prefix.Addr().String(),
			},
			"credential_policy": credentialPolicy,
			"model_access": map[string]any{
				"model_access_allowed": modelAccessAllowed,
			},
			"driver_runtime": map[string]any{
				"driver_home_mount":             "/agent-home",
				"generated_driver_config_mount": "/harness-control/driver/" + allocation.DriverState.DriverID,
				"materialized_driver_config":    runtimeMaterialized,
				"initial_driver_state_digest":   allocation.DriverState.StateDigest,
			},
			"mount_plan": map[string]any{
				"driver_config_materializations": mountMaterialized,
			},
			"input_digests": map[string]any{
				"runtime_config_digest": "sha256:runtime-config",
				"rootfs_image_digest":   nil,
				"agent_manifest_digest": "sha256:agent-manifest",
			},
		},
		ContractGateVersion: store.SandboxContractGateDriverManifest,
		Now:                 now,
	}); err != nil {
		t.Fatalf("store sandbox contract: %v", err)
	}
	artifacts := testGenerationArtifacts()
	instance, err := st.CreateRuntimeResourceInstance(ctx, store.RuntimeResourceInstanceParams{
		GenerationID:           allocation.GenerationID,
		SessionID:              sessionID,
		ContractID:             contractID,
		SandboxContractVersion: store.SandboxContractVersion,
		HostID:                 hostID,
		RunscContainerID:       details.RunscContainerID,
		RunscPlatform:          "systrap",
		RunscVersion:           artifacts.RunscVersion,
		RunscBinaryPath:        artifacts.RunscBinaryPath,
		RunscBinaryDigest:      artifacts.RunscBinaryDigest,
		NetworkProfileID:       allocation.NetworkProfileID,
		NetnsName:              details.NetnsName,
		NetnsPath:              details.NetnsPath,
		HostVeth:               details.HostVeth,
		SandboxVeth:            details.SandboxVeth,
		HostGatewayIP:          details.HostGatewayIP,
		SandboxIP:              prefix.Addr().String(),
		SandboxIPCIDR:          details.SandboxIPCIDR,
		HostSideCIDR:           details.HostSideCIDR,
		NftTableName:           runtimeResourceNftTableName(allocation.GenerationID),
		ControlDirPath:         details.ControlDirPath,
		ControlManifestPath:    details.ControlManifestPath,
		BundleDirPath:          details.BundleDirPath,
		SpecPath:               details.SpecPath,
		CheckpointPath:         details.CheckpointPath,
		BridgeDirPath:          details.BridgeDirPath,
		NetworkHostsPath:       details.NetworkHostsPath,
		LogDirPath:             details.LogDirPath,
		RootPrefixes: map[string]string{
			"run_dir":      filepath.Dir(filepath.Dir(details.ControlDirPath)),
			"control_root": filepath.Dir(details.ControlDirPath),
			"bridge_root":  filepath.Dir(details.BridgeDirPath),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("create runtime resource instance: %v", err)
	}
	workerID := strings.TrimSpace(ownerUUID)
	if workerID == "" {
		workerID = strings.TrimSuffix(strings.TrimSpace(allocation.Owner), ":"+store.RuntimeManagerRoleTag)
	}
	if err := st.ClaimRuntimeResourceMaterialization(ctx, store.RuntimeResourceMaterializationClaimParams{
		GenerationID:     allocation.GenerationID,
		WorkerID:         workerID,
		HostID:           hostID,
		LeaseExpiresAt:   now.Add(time.Minute),
		IdempotencyToken: "test:" + allocation.GenerationID,
		Now:              now.Add(time.Millisecond),
	}); err != nil {
		t.Fatalf("claim runtime resource materialization: %v", err)
	}
	if err := st.MarkRuntimeResourceReady(ctx, store.RuntimeResourceWorkerTransitionParams{
		GenerationID: allocation.GenerationID,
		WorkerID:     workerID,
		HostID:       hostID,
		Now:          now.Add(2 * time.Millisecond),
	}); err != nil {
		t.Fatalf("mark runtime resource ready: %v", err)
	}
	if err := st.MarkRuntimeResourceLive(ctx, store.RuntimeResourceWorkerTransitionParams{
		GenerationID: allocation.GenerationID,
		WorkerID:     workerID,
		HostID:       hostID,
		PostStart:    serverPostStartProofForTest(instance),
		Now:          now.Add(3 * time.Millisecond),
	}); err != nil {
		t.Fatalf("mark runtime resource live: %v", err)
	}
	return instance
}

func serverCredentialPolicyForTest(t *testing.T, driverID string) map[string]any {
	t.Helper()
	secretGrants := []map[string]any{}
	if driverID == "pi" {
		secretGrants = append(secretGrants, map[string]any{
			"grant_id":                  "model_provider:anthropic_proxy",
			"domain":                    "model_provider",
			"scope":                     "anthropic_messages",
			"exposure_mode":             "proxy_only",
			"ttl_seconds":               nil,
			"allowed_drivers":           []string{driverID},
			"allowed_runtime_providers": []string{"local_runsc"},
		})
	}
	policy := map[string]any{
		"provider_credentials": "host-only",
		"sandbox_secret_mount": "absent",
		"proxy_token":          "absent",
		"secret_grants":        secretGrants,
	}
	digest, err := store.CredentialPolicyDigest(policy)
	if err != nil {
		t.Fatalf("credential digest: %v", err)
	}
	policy["digest"] = digest
	return policy
}

func serverPostStartProofForTest(instance store.RuntimeResourceInstance) *store.RuntimeResourcePostStartProof {
	return &store.RuntimeResourcePostStartProof{
		HostID:                 instance.HostID,
		GenerationID:           instance.GenerationID,
		ContractID:             instance.ContractID,
		SandboxContractVersion: instance.SandboxContractVersion,
		RunscContainerID:       instance.RunscContainerID,
		RunscState:             "runsc_container:" + instance.RunscContainerID + ":running; check=test",
		RunscPlatform:          instance.RunscPlatform,
		RunscVersion:           instance.RunscVersion,
		RunscBinaryPath:        instance.RunscBinaryPath,
		RunscBinaryDigest:      instance.RunscBinaryDigest,
		IPNetns:                "netns:present; check=test",
		IPLink:                 "host_veth:present; check=test",
		NFT:                    "nft_table:present; check=test",
		BridgeStartup:          "bridge_startup_probe:passed; check=test",
	}
}

type serverCheckpointImageManifest struct {
	Version int                                 `json:"version"`
	Files   []serverCheckpointImageManifestFile `json:"files"`
}

type serverCheckpointImageManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func writeServerCheckpointFilesWithoutManifest(t *testing.T, checkpointPath string) {
	t.Helper()
	if err := os.MkdirAll(checkpointPath, 0o755); err != nil {
		t.Fatalf("create checkpoint path: %v", err)
	}
	for _, name := range []string{"checkpoint.img", "pages.img", "pages_meta.img"} {
		if err := os.WriteFile(filepath.Join(checkpointPath, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write checkpoint file %s: %v", name, err)
		}
	}
}

func buildServerCheckpointImageManifest(checkpointPath string) (serverCheckpointImageManifest, error) {
	manifest := serverCheckpointImageManifest{Version: 1}
	for _, name := range []string{"checkpoint.img", "pages.img", "pages_meta.img"} {
		path := filepath.Join(checkpointPath, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return serverCheckpointImageManifest{}, err
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, serverCheckpointImageManifestFile{
			Path:   name,
			Size:   int64(len(data)),
			SHA256: fmt.Sprintf("%x", sum),
		})
	}
	return manifest, nil
}

func writeServerJSONFile(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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
		RestoreID: "phase3-" + id,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: expiresAt,
	}
	if err := os.MkdirAll(session.Workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session
}

func serverRunscContainerID(t *testing.T, ctx context.Context, st *store.Store, sessionID, generationID string) string {
	t.Helper()
	details, err := st.GetRuntimeGenerationDetails(ctx, sessionID, generationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	if strings.TrimSpace(details.RunscContainerID) == "" {
		t.Fatalf("generation %s has no runsc container id", generationID)
	}
	return details.RunscContainerID
}

func enableSessionAutoCheckpoint(t *testing.T, ctx context.Context, st *store.Store, sessionID string) {
	t.Helper()
	if _, err := st.DBForTest().ExecContext(ctx, `UPDATE sessions SET auto_checkpoint_enabled = 1 WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("enable auto checkpoint: %v", err)
	}
}

func prepareServerIdleGeneration(t *testing.T, ctx context.Context, st *store.Store, cfg config.Config, ownerUUID, sessionID string) store.GenerationAllocation {
	t.Helper()
	now := time.Now().UTC()
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: sessionID,
		Owner:     store.GenerationLeaseOwner(ownerUUID),
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	artifacts := testGenerationArtifacts()
	if err := st.RecordGenerationRuntimeArtifactDigests(ctx, allocation.GenerationID, store.GenerationRuntimeArtifactDigests{
		ControlManifestDigest:          artifacts.ManifestDigest,
		ProjectedControlManifestDigest: artifacts.ProjectedManifestDigest,
		BundleDigest:                   artifacts.BundleDigest,
		RuntimeConfigDigest:            artifacts.RuntimeConfigDigest,
		SpecDigest:                     artifacts.SpecDigest,
		RunscVersion:                   artifacts.RunscVersion,
		RunscBinaryPath:                artifacts.RunscBinaryPath,
		RunscBinaryDigest:              artifacts.RunscBinaryDigest,
	}); err != nil {
		t.Fatalf("record runtime artifacts: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, sessionID, allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	createServerRuntimeResourceLive(t, ctx, st, sessionID, allocation, ownerUUID, "host-idle", now.Add(2*time.Second))
	if err := st.UpdateSessionStatusAndActivity(ctx, sessionID, string(sessionstate.RunningIdle), nil, now.Add(-time.Minute)); err != nil {
		t.Fatalf("mark session idle: %v", err)
	}
	return allocation
}

func markServerGenerationCheckpointed(t *testing.T, ctx context.Context, st *store.Store, sessionID, generationID string, now time.Time) {
	t.Helper()
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	fence := serverCheckpointDriverStateFenceForTest(t, ctx, st, sessionID, generationID)
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'checkpointed',
    checkpoint_created_at = ?,
    checkpoint_network_profile_id = network_profile_id,
    checkpoint_agent_runtime_profile_id = agent_runtime_profile_id,
    checkpoint_runsc_version = COALESCE(runsc_version, 'runsc test'),
    checkpoint_runsc_platform = COALESCE(runsc_platform, 'systrap'),
    checkpoint_runsc_binary_path = COALESCE((
      SELECT NULLIF(runsc_binary_path, '')
      FROM runtime_generation_resources
      WHERE runtime_generation_resources.generation_id = runtime_generations.generation_id
    ), '/usr/local/bin/runsc-test'),
    checkpoint_runsc_binary_digest = COALESCE((
      SELECT NULLIF(runsc_binary_digest, '')
      FROM runtime_generation_resources
      WHERE runtime_generation_resources.generation_id = runtime_generations.generation_id
    ), 'sha256:runsc-test'),
    checkpoint_bundle_digest = 'bundle_digest',
    checkpoint_runtime_config_digest = 'runtime_config_digest',
    checkpoint_control_manifest_digest = COALESCE((
      SELECT control_manifest_digest
      FROM runtime_generation_resources
      WHERE runtime_generation_resources.generation_id = runtime_generations.generation_id
    ), 'manifest_digest'),
    checkpoint_driver_states_digest = ?,
    lease_owner = NULL,
    lease_expires_at = NULL,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?`, formattedNow, fence, formattedNow, generationID, sessionID); err != nil {
		t.Fatalf("set checkpointed generation: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reserved_checkpointed'
WHERE generation_id = ?
  AND session_id = ?`, generationID, sessionID); err != nil {
		t.Fatalf("reserve checkpointed network: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reserved_checkpointed'
WHERE generation_id = ?`, generationID); err != nil {
		t.Fatalf("reserve checkpointed resources: %v", err)
	}
	if err := st.UpdateSessionStatus(ctx, sessionID, string(sessionstate.Checkpointed), nil); err != nil {
		t.Fatalf("set checkpointed session: %v", err)
	}
}

func serverCheckpointDriverStateFenceForTest(t *testing.T, ctx context.Context, st *store.Store, sessionID, generationID string) string {
	t.Helper()
	var driverID, stateDigest string
	var stateVersion int
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT ds.driver_id, ds.state_digest, ds.state_version
FROM session_driver_states ds
JOIN runtime_generations g ON g.session_id = ds.session_id
JOIN agent_runtime_profiles a ON a.agent_runtime_profile_id = g.agent_runtime_profile_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND ds.driver_id = a.driver_id`, sessionID, generationID).Scan(&driverID, &stateDigest, &stateVersion); err != nil {
		t.Fatalf("query driver state fence input: %v", err)
	}
	fence, err := store.CheckpointDriverStatesDigest(generationID, []store.DriverStateToken{{
		DriverID:     driverID,
		StateDigest:  stateDigest,
		StateVersion: stateVersion,
	}})
	if err != nil {
		t.Fatalf("compute driver state fence: %v", err)
	}
	return fence
}

func newServerTestWatcher(t *testing.T, sessionsRoot string, st *store.Store, hub *events.Hub) *artifacts.Watcher {
	t.Helper()
	return artifacts.New(localworkspace.DataVolumeProvisionerConfig{
		SessionsRoot:   sessionsRoot,
		AgentHomesRoot: filepath.Join(t.TempDir(), "agent-homes"),
		EvidenceRoot:   filepath.Join(t.TempDir(), "volume-evidence"),
		RuntimeIdentity: store.RuntimeIdentity{
			UID: serverTestSandboxUID(),
			GID: serverTestSandboxGID(),
		},
	}, st, hub, slog.Default())
}

func testServerConfig(dir string) config.Config {
	return config.Config{
		SessionsRoot:     filepath.Join(dir, "sessions"),
		AgentHomesRoot:   filepath.Join(dir, "agent-homes"),
		CheckpointsRoot:  filepath.Join(dir, "checkpoints"),
		BundleRoot:       filepath.Join(dir, "bundle"),
		RootFSPath:       filepath.Join(dir, "rootfs"),
		DBPath:           filepath.Join(dir, "state", "orchestrator.db"),
		RepoRoot:         dir,
		SessionRetention: time.Hour,
		MaxSessions:      10,
		DefaultAgent:     "pi",
		ModelProxy: config.ModelProxyConfig{
			BindURL:        "http://0.0.0.0:8082",
			SandboxBaseURL: "http://harness-model-proxy.internal:8082",
			BindPort:       8082,
		},
		Harness: config.HarnessConfig{
			RunDir: filepath.Join(dir, "run"),
			Network: config.NetworkConfig{
				CIDRPool: config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/29")},
				Egress: config.EgressConfig{
					DorisFEHosts: []string{"192.0.2.10"},
					DorisBEHosts: []string{"172.16.0.139"},
					DorisPorts:   []int{9030},
					DNSPolicy:    config.DNSPolicyHostnamesOnly,
				},
			},
			Bridge: config.BridgeConfig{
				LeaseTTL:          config.Duration{Duration: time.Minute},
				HeartbeatInterval: config.Duration{Duration: 10 * time.Millisecond},
				PollInterval:      config.Duration{Duration: 10 * time.Millisecond},
				AckStartedGrace:   config.Duration{Duration: 90 * time.Second},
				ReconnectGrace:    config.Duration{Duration: 30 * time.Second},
			},
			Events: config.EventsConfig{
				RetentionWindow:        config.Duration{Duration: time.Hour},
				RetentionRows:          1_000,
				EmitOutputBatchMaxRows: 64,
				EmitOutputBatchMaxAge:  config.Duration{Duration: 100 * time.Millisecond},
			},
			Reaper: config.ReaperConfig{
				FailedRetention: config.Duration{Duration: 0},
			},
			Maintenance: config.MaintenanceConfig{
				StageTimeout:         config.Duration{Duration: 10 * time.Second},
				ColdFallbackInterval: config.Duration{Duration: 10 * time.Millisecond},
			},
			SandboxIdentity: config.SandboxIdentity{
				UID: serverTestSandboxUID(),
				GID: serverTestSandboxGID(),
			},
			ProxyServiceIdentity: config.ProxyServiceIdentity{
				UID: os.Geteuid(),
				GID: os.Getegid(),
			},
		},
	}
}

func serverTestSandboxUID() int {
	uid := os.Getuid()
	if uid > 0 {
		return uid
	}
	return 65534
}

func serverTestSandboxGID() int {
	gid := os.Getgid()
	if gid > 0 {
		return gid
	}
	return 65534
}

func TestResourceAllocatorConfigUsesHostOnlyModelCredentials(t *testing.T) {
	cfg := testServerConfig(t.TempDir())
	cfg.ModelProxy.SandboxBaseURL = "http://harness-model-proxy.internal:8082"
	srv := &Server{cfg: cfg}

	agentConfig := srv.resourceAllocatorConfig("pi")
	if !agentConfig.ProviderCredentialsHostOnly {
		t.Fatalf("agent allocator should keep provider credentials host-only: %+v", agentConfig)
	}
	if agentConfig.SandboxModelProxyBaseURL != "http://harness-model-proxy.internal:8082" {
		t.Fatalf("agent sandbox model proxy base url = %q", agentConfig.SandboxModelProxyBaseURL)
	}
	if agentConfig.SandboxUID != cfg.Harness.SandboxIdentity.UID ||
		agentConfig.SandboxGID != cfg.Harness.SandboxIdentity.GID {
		t.Fatalf("agent allocator sandbox identity = %+v", agentConfig)
	}

	shellConfig := srv.resourceAllocatorConfig("sh")
	if shellConfig.ProviderCredentialsHostOnly || shellConfig.SandboxModelProxyBaseURL != "http://harness-model-proxy.internal:8082" {
		t.Fatalf("shell allocator should not request host-only provider credentials: %+v", shellConfig)
	}
}

func TestResourceAllocatorConfigUsesModelProxyPort(t *testing.T) {
	cfg := testServerConfig(t.TempDir())
	cfg.ModelProxy = config.ModelProxyConfig{
		BindURL:        "http://0.0.0.0:8083",
		SandboxBaseURL: "http://harness-model-proxy.internal:8083",
		BindPort:       8083,
	}
	srv := &Server{cfg: cfg}

	allocatorConfig := srv.resourceAllocatorConfig("pi")
	if allocatorConfig.HostProxyBindURL != "http://0.0.0.0:8083" ||
		allocatorConfig.ProxyPort != 8083 ||
		allocatorConfig.SandboxModelProxyBaseURL != "http://harness-model-proxy.internal:8083" {
		t.Fatalf("allocator model proxy config = %+v", allocatorConfig)
	}
}

func serverTestAllocatorConfig(cfg config.Config, agent string) store.ResourceAllocatorConfig {
	outputFormat := "stream-json"
	modelAccess := false
	if spec, ok := agents.DriverSpecFor(agent); ok {
		outputFormat = spec.OutputFormat
		modelAccess = spec.ModelAccess
	}
	return store.ResourceAllocatorConfig{
		RunDir:                      cfg.Harness.RunDir,
		CIDRPool:                    cfg.Harness.Network.CIDRPool.Prefix,
		EgressDorisFEHosts:          cfg.Harness.Network.Egress.DorisFEHosts,
		EgressDorisBEHosts:          cfg.Harness.Network.Egress.DorisBEHosts,
		EgressDorisPorts:            cfg.Harness.Network.Egress.DorisPorts,
		EgressDNSPolicy:             string(cfg.Harness.Network.Egress.DNSPolicy),
		HostProxyBindURL:            cfg.ModelProxy.BindURL,
		ProxyPort:                   cfg.ModelProxy.BindPort,
		Agent:                       agent,
		AgentModel:                  "sonnet",
		AgentOutputFormat:           outputFormat,
		DisableNonessentialTraffic:  true,
		SandboxUID:                  cfg.Harness.SandboxIdentity.UID,
		SandboxGID:                  cfg.Harness.SandboxIdentity.GID,
		SandboxSupplementalGIDs:     cfg.Harness.SandboxIdentity.SupplementalGIDs,
		ProviderCredentialsHostOnly: modelAccess,
		SandboxModelProxyBaseURL:    cfg.ModelProxy.SandboxBaseURL,
	}
}

func createServerGenerationFilesystem(t *testing.T, details store.RuntimeGenerationDetails) {
	t.Helper()
	for _, path := range []string{details.CheckpointPath, details.ControlDirPath, details.BundleDirPath, details.BridgeDirPath, details.LogDirPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create generation filesystem path %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(path, ".keep"), []byte("keep"), 0o644); err != nil {
			t.Fatalf("write generation filesystem marker %s: %v", path, err)
		}
	}
}

// waitForDestroyRequest blocks until the runtime has been asked to destroy the
// given generation. Use it when a background loop tears a sandbox down after the
// DB transition a test synchronised on, so the test does not cancel the loop's
// context out from under the teardown it is about to assert.
func waitForDestroyRequest(t *testing.T, rt *recordingRuntime, generationID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, destroy := range rt.destroyGenerationRequests() {
			if destroy.GenerationID == generationID {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime was never asked to destroy generation %s, got %+v", generationID, rt.destroyGenerationRequests())
}

func waitForSessionStatus(t *testing.T, ctx context.Context, st *store.Store, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := st.GetSession(ctx, sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if got.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := st.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("get final session: %v", err)
	}
	data, _ := json.Marshal(got)
	t.Fatalf("session did not reach %s: %s", want, data)
}

func waitForGenerationResourceStates(t *testing.T, ctx context.Context, st *store.Store, generationID, wantNetwork, wantResource string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var networkState, resourceState string
		if err := st.DBForTest().QueryRowContext(ctx, `
SELECT n.allocation_state, r.resource_state
FROM network_profiles n
JOIN runtime_generation_resources r ON r.generation_id = n.generation_id
WHERE n.generation_id = ?`, generationID).Scan(&networkState, &resourceState); err != nil {
			t.Fatalf("query generation resource states: %v", err)
		}
		if networkState == wantNetwork && resourceState == wantResource {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var networkState, resourceState string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT n.allocation_state, r.resource_state
FROM network_profiles n
JOIN runtime_generation_resources r ON r.generation_id = n.generation_id
WHERE n.generation_id = ?`, generationID).Scan(&networkState, &resourceState); err != nil {
		t.Fatalf("query final generation resource states: %v", err)
	}
	t.Fatalf("generation %s resource states did not reach %s/%s: network=%s resource=%s", generationID, wantNetwork, wantResource, networkState, resourceState)
}

func waitForGenerationStatus(t *testing.T, ctx context.Context, st *store.Store, generationID, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var got string
		if err := st.DBForTest().QueryRowContext(ctx, `SELECT status FROM runtime_generations WHERE generation_id = ?`, generationID).Scan(&got); err != nil {
			t.Fatalf("query generation status: %v", err)
		}
		if got == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled before generation reached %s", want)
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got string
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT status FROM runtime_generations WHERE generation_id = ?`, generationID).Scan(&got); err != nil {
		t.Fatalf("query final generation status: %v", err)
	}
	t.Fatalf("generation did not reach %s: got %s", want, got)
}

func waitForGenerationLeaseAfter(t *testing.T, ctx context.Context, st *store.Store, generationID string, after time.Time) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var raw string
		if err := st.DBForTest().QueryRowContext(ctx, `SELECT lease_expires_at FROM runtime_generations WHERE generation_id = ?`, generationID).Scan(&raw); err != nil {
			t.Fatalf("query generation lease: %v", err)
		}
		if got, err := time.Parse(time.RFC3339Nano, raw); err == nil && got.After(after) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled before generation lease renewed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	var raw string
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT lease_expires_at FROM runtime_generations WHERE generation_id = ?`, generationID).Scan(&raw); err != nil {
		t.Fatalf("query final generation lease: %v", err)
	}
	t.Fatalf("generation %s lease was not renewed after %s: got %s", generationID, after, raw)
}

func waitForEventIDs(t *testing.T, ctx context.Context, st *store.Store, want []int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		records, err := st.ListEvents(ctx, store.ListEventsParams{})
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		got := make([]int64, 0, len(records))
		for _, record := range records {
			got = append(got, record.EventID)
		}
		if int64sEqual(got, want) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled before retained events reached %v", want)
		case <-time.After(10 * time.Millisecond):
		}
	}
	records, err := st.ListEvents(context.Background(), store.ListEventsParams{})
	if err != nil {
		t.Fatalf("list final events: %v", err)
	}
	got := make([]int64, 0, len(records))
	for _, record := range records {
		got = append(got, record.EventID)
	}
	t.Fatalf("event ids=%v want %v", got, want)
}

func int64sEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func createServerRunningProxyTurn(t *testing.T, ctx context.Context, st *store.Store, cfg config.Config, ownerUUID, dir, sessionID string, now time.Time) (store.GenerationAllocation, int64, string) {
	t.Helper()
	createServerTestSession(t, ctx, st, dir, sessionID, string(sessionstate.RunningActive), now, nil)
	owner := store.GenerationLeaseOwner(ownerUUID)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: sessionID,
		Owner:     owner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, sessionID, allocation.GenerationID, allocation.Owner, now.Add(time.Second)); err != nil {
		t.Fatalf("mark resources live: %v", err)
	}
	createServerRuntimeResourceLive(t, ctx, st, sessionID, allocation, ownerUUID, "host-proxy", now.Add(2*time.Second))
	turnID, err := st.EnqueueTurn(ctx, sessionID, "proxy observed turn", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{
		SessionID:    sessionID,
		GenerationID: allocation.GenerationID,
		Owner:        allocation.Owner,
		RequestID:    "claim_" + sessionID,
		LeaseTTL:     time.Minute,
		Now:          now.Add(3 * time.Second),
	})
	if err != nil || !ok || grant.TurnID != turnID {
		t.Fatalf("claim setup: ok=%v grant=%+v err=%v", ok, grant, err)
	}
	sandboxSourceIP := serverSandboxSourceIPForGeneration(t, ctx, st, allocation.GenerationID)
	if _, err := st.AckTurnStarted(ctx, store.AckStartedParams{
		SessionID:       sessionID,
		GenerationID:    allocation.GenerationID,
		TurnID:          turnID,
		Owner:           allocation.Owner,
		SandboxSourceIP: sandboxSourceIP,
		LeaseTTL:        time.Minute,
		Now:             now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("ack turn started: %v", err)
	}
	return allocation, turnID, sandboxSourceIP
}

func serverSandboxSourceIPForGeneration(t *testing.T, ctx context.Context, st *store.Store, generationID string) string {
	t.Helper()
	var sandboxCIDR string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT sandbox_ip_cidr
FROM network_profiles
WHERE generation_id = ?`, generationID).Scan(&sandboxCIDR); err != nil {
		t.Fatalf("query sandbox ip cidr: %v", err)
	}
	parts := strings.SplitN(sandboxCIDR, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		t.Fatalf("unexpected sandbox ip cidr: %q", sandboxCIDR)
	}
	return parts[0]
}

func waitForHubEvent(t *testing.T, ch <-chan events.Event, eventType string) events.Event {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-ch:
			if event.Type == eventType {
				return event
			}
		case <-deadline:
			t.Fatalf("timeout waiting for hub event %s", eventType)
		}
	}
}

func assertProxyCorrelationSocketPermissions(t *testing.T, socketPath string, proxyServiceGID int) {
	t.Helper()
	for _, check := range []struct {
		name string
		path string
		mode os.FileMode
	}{
		{name: "socket root", path: filepath.Dir(socketPath), mode: 0o750},
		{name: "socket", path: socketPath, mode: 0o660},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatalf("stat proxy correlation %s: %v", check.name, err)
		}
		if info.Mode().Perm() != check.mode {
			t.Fatalf("proxy correlation %s mode=%#o want %#o", check.name, info.Mode().Perm(), check.mode)
		}
		if os.Geteuid() != 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("proxy correlation %s stat type = %T", check.name, info.Sys())
		}
		if stat.Uid != 0 || stat.Gid != uint32(proxyServiceGID) {
			t.Fatalf("proxy correlation %s ownership=%d:%d want 0:%d", check.name, stat.Uid, stat.Gid, proxyServiceGID)
		}
	}
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

func assertContains(t *testing.T, value, want string) {
	t.Helper()
	if !strings.Contains(value, want) {
		t.Fatalf("expected %q to contain %q", value, want)
	}
}

func jsonArrayContainsAll(values []any, want ...string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		if text, ok := value.(string); ok {
			seen[text] = struct{}{}
		}
	}
	for _, value := range want {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func drainHasEvent(ch <-chan events.Event, eventType string) bool {
	for {
		select {
		case event := <-ch:
			if event.Type == eventType {
				return true
			}
		default:
			return false
		}
	}
}

func TestDownloadArtifactRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	srv, _, workspace := newArtifactDownloadServer(t, "sess_1")
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "outside.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/outside.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDownloadArtifactRejectsSymlinkDirectory(t *testing.T) {
	dir := t.TempDir()
	srv, _, workspace := newArtifactDownloadServer(t, "sess_1")
	outsideDir := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(workspace, "linked")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/linked/secret.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDownloadArtifactRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{cfg: config.Config{SessionsRoot: dir}}
	req := httptest.NewRequest(http.MethodGet, "/artifacts/sess_1/../outside.txt", nil)
	rec := httptest.NewRecorder()

	srv.getArtifact(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body %s", rec.Code, rec.Body.String())
	}
}

// The dataplane authorize route must sit behind the same SO_PEERCRED gate as the
// model-proxy correlation routes, and must not be reachable on the public mux.
// A sandbox can reach the gateway; it must not be able to reach this.
func TestDataProxyAuthorizeRouteIsGatedAndAuthorizes(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "hp-dataproxy-")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	now := time.Now().UTC()
	allocation, turnID, sandboxSourceIP := createServerRunningProxyTurn(t, ctx, st, cfg, owner.UUID, dir, "sess_dataproxy_http", now)

	hub := events.NewHub()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, hub),
		hub:     hub,
		log:     slog.Default(),
	}

	body := fmt.Sprintf(`{"domain":"doris","sandbox_source_ip":%q}`, sandboxSourceIP)

	// Not on the public mux at all.
	publicRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(publicRec, httptest.NewRequest(http.MethodPost,
		"/internal/dataplane/requests/authorize", strings.NewReader(body)))
	if publicRec.Code != http.StatusNotFound {
		t.Fatalf("public dataplane route status=%d body=%s", publicRec.Code, publicRec.Body.String())
	}

	// On the correlation mux, but refused without peer credentials.
	directRec := httptest.NewRecorder()
	srv.ProxyCorrelationRoutes().ServeHTTP(directRec, httptest.NewRequest(http.MethodPost,
		"/internal/dataplane/requests/authorize", strings.NewReader(body)))
	if directRec.Code != http.StatusForbidden {
		t.Fatalf("dataplane route without peer credentials status=%d body=%s", directRec.Code, directRec.Body.String())
	}

	// Over the Unix socket, with peer credentials, it authorizes.
	listener, _, err := srv.ListenProxyCorrelation()
	if err != nil {
		t.Fatalf("listen proxy correlation: %v", err)
	}
	proxyServer := srv.ProxyCorrelationServer()
	go func() { _ = proxyServer.Serve(listener) }()
	t.Cleanup(func() { _ = proxyServer.Close() })

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
		},
	}}
	resp, err := client.Post("http://unix/internal/dataplane/requests/authorize",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("authorize over socket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("authorize status=%d body=%s", resp.StatusCode, payload)
	}
	var decoded struct {
		Authorized   bool   `json:"authorized"`
		SessionID    string `json:"session_id"`
		GenerationID string `json:"generation_id"`
		TurnID       int64  `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.Authorized || decoded.SessionID != "sess_dataproxy_http" ||
		decoded.GenerationID != allocation.GenerationID || decoded.TurnID != turnID {
		t.Errorf("decoded = %+v, want an authorization for turn %d", decoded, turnID)
	}

	// An unknown source IP is a 403 carrying a reason the agent can act on,
	// never a 200 with authorized=false.
	denyResp, err := client.Post("http://unix/internal/dataplane/requests/authorize",
		"application/json", strings.NewReader(`{"domain":"doris","sandbox_source_ip":"10.255.255.254"}`))
	if err != nil {
		t.Fatalf("deny request: %v", err)
	}
	defer denyResp.Body.Close()
	if denyResp.StatusCode != http.StatusForbidden {
		t.Errorf("unknown source ip status=%d, want 403", denyResp.StatusCode)
	}
	var denied struct {
		Authorized bool   `json:"authorized"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(denyResp.Body).Decode(&denied); err != nil {
		t.Fatalf("decode denial: %v", err)
	}
	if denied.Authorized || denied.Reason == "" {
		t.Errorf("denial = %+v, want authorized=false with a reason", denied)
	}

	// A missing source IP is a client error, never a grant.
	badResp, err := client.Post("http://unix/internal/dataplane/requests/authorize",
		"application/json", strings.NewReader(`{"domain":"doris"}`))
	if err != nil {
		t.Fatalf("bad request: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing source ip status=%d, want 400", badResp.StatusCode)
	}
}

// hangingDestroyRuntime blocks forever in Destroy, reproducing the 2026-08-17
// outage: an OOM-killed sandbox left runsc unresponsive, so the `runsc kill`
// inside Destroy never returned.
type hangingDestroyRuntime struct {
	recordingRuntime
	destroyEntered chan struct{}
	release        chan struct{}
	enteredOnce    sync.Once
}

func newHangingDestroyRuntime() *hangingDestroyRuntime {
	return &hangingDestroyRuntime{
		destroyEntered: make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (r *hangingDestroyRuntime) Destroy(ctx context.Context, runtimeID string) error {
	r.mu.Lock()
	r.destroyRuntimeIDs = append(r.destroyRuntimeIDs, runtimeID)
	r.mu.Unlock()
	r.enteredOnce.Do(func() { close(r.destroyEntered) })
	// Deliberately ignore ctx: a wedged runsc CLI does not observe cancellation
	// either. Only the test releases this.
	<-r.release
	return nil
}

// TestRunMaintenanceColdFallbackSurvivesWedgedRecoveryStage is the regression
// test for the 34h outage. Before stage isolation, runMaintenance ran every step
// serially on one goroutine, so a Destroy that never returned starved every
// later step — including startColdFallbackSessions, which is what starts a
// sandbox for a session whose generation died with turns still queued. The
// result was that no user could start a turn at all.
//
// Here recovery is wedged permanently while a separate session needs cold
// fallback. Cold fallback must still run.
func TestRunMaintenanceColdFallbackSurvivesWedgedRecoveryStage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	cfg.Harness.Network.CIDRPool = config.CIDRPrefix{Prefix: netip.MustParsePrefix("10.241.0.0/26")}
	cfg.Harness.Bridge.HeartbeatInterval = config.Duration{Duration: 10 * time.Millisecond}
	cfg.Harness.Bridge.ReconnectGrace = config.Duration{Duration: 10 * time.Millisecond}
	cfg.Harness.Maintenance.ColdFallbackInterval = config.Duration{Duration: 10 * time.Millisecond}
	// Long enough that the wedged stage stays wedged for the whole test: we are
	// asserting isolation, not that the deadline eventually frees it.
	cfg.Harness.Maintenance.StageTimeout = config.Duration{Duration: time.Minute}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	now := time.Now().UTC()

	// Session A: an expired-lease generation for recovery to trip over. Its
	// Destroy call is the one that hangs.
	stuckSession := createServerTestSession(t, ctx, st, dir, "sess_wedged_recovery", string(sessionstate.RunningIdle), now, nil)
	stuck, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: stuckSession.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate stuck generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, stuckSession.ID, stuck.GenerationID, stuck.Owner, now); err != nil {
		t.Fatalf("mark stuck generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = ?, lease_expires_at = ?, sandbox_last_seen_at = ?
WHERE generation_id = ?`,
		leaseOwner,
		now.Add(-time.Minute).Format(time.RFC3339Nano),
		now.Add(-time.Hour).Format(time.RFC3339Nano),
		stuck.GenerationID,
	); err != nil {
		t.Fatalf("expire stuck generation lease: %v", err)
	}

	// Session B: a failed generation with a queued turn — the cold-fallback case.
	waitingSession := createServerTestSession(t, ctx, st, dir, "sess_needs_fallback", string(sessionstate.RunningIdle), now, nil)
	old, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: waitingSession.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate old generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, waitingSession.ID, old.GenerationID, old.Owner, now); err != nil {
		t.Fatalf("mark old generation live: %v", err)
	}
	if err := st.FailGeneration(ctx, store.FailGenerationParams{
		SessionID:    waitingSession.ID,
		GenerationID: old.GenerationID,
		Owner:        old.Owner,
		ErrorClass:   "sandbox_oom",
		Reason:       "sandbox_oom",
		Now:          now,
	}); err != nil {
		t.Fatalf("fail old generation: %v", err)
	}
	if _, err := st.EnqueueTurn(ctx, waitingSession.ID, "message that must not hang forever", now); err != nil {
		t.Fatalf("enqueue queued turn: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt := newHangingDestroyRuntime()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()
	defer func() {
		close(rt.release)
		cancel()
		<-done
	}()

	// Recovery must actually be wedged, otherwise the test proves nothing.
	select {
	case <-rt.destroyEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery stage never called Destroy; test cannot prove isolation")
	}

	// The payoff: cold fallback runs while recovery is stuck forever. Wait for the
	// replacement generation AND the status flip — startColdFallbackSessions
	// writes them in separate steps, so checking the status the moment the new
	// generation appears would race.
	deadline := time.Now().Add(10 * time.Second)
	var got store.Session
	for {
		var err error
		got, err = st.GetSession(ctx, waitingSession.ID)
		if err != nil {
			t.Fatalf("get waiting session: %v", err)
		}
		replaced := got.ActiveGenerationID != "" && got.ActiveGenerationID != old.GenerationID
		if replaced && got.Status == string(sessionstate.RunningActive) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cold fallback did not complete while the recovery stage was wedged: session=%+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And the queued turn is now claimable on the new sandbox: the user's message
	// actually proceeds rather than sitting in the queue forever.
	grant, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{
		SessionID:    waitingSession.ID,
		GenerationID: got.ActiveGenerationID,
		Owner:        leaseOwner,
		RequestID:    "req_wedged_isolation_claim",
		LeaseTTL:     time.Minute,
		Now:          time.Now().UTC(),
	})
	if err != nil || !ok {
		t.Fatalf("queued turn not claimable after fallback: ok=%v err=%v", ok, err)
	}
	if grant.Content != "message that must not hang forever" {
		t.Fatalf("claimed unexpected turn: %+v", grant)
	}
}

// A stage that overruns its deadline must not be started again concurrently.
func TestRunMaintenanceSkipsStageAlreadyRunning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	cfg := testServerConfig(dir)
	cfg.Harness.Bridge.HeartbeatInterval = config.Duration{Duration: 10 * time.Millisecond}
	cfg.Harness.Bridge.ReconnectGrace = config.Duration{Duration: 10 * time.Millisecond}
	// Short deadline so the loop gives up on the wedged stage and keeps ticking.
	cfg.Harness.Maintenance.StageTimeout = config.Duration{Duration: 50 * time.Millisecond}
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	now := time.Now().UTC()

	session := createServerTestSession(t, ctx, st, dir, "sess_stage_reentry", string(sessionstate.RunningIdle), now, nil)
	gen, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     leaseOwner,
		LeaseTTL:  time.Minute,
		Now:       now,
		Config:    serverTestAllocatorConfig(cfg, "pi"),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, gen.GenerationID, gen.Owner, now); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET lease_owner = ?, lease_expires_at = ?, sandbox_last_seen_at = ?
WHERE generation_id = ?`,
		leaseOwner,
		now.Add(-time.Minute).Format(time.RFC3339Nano),
		now.Add(-time.Hour).Format(time.RFC3339Nano),
		gen.GenerationID,
	); err != nil {
		t.Fatalf("expire generation lease: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt := newHangingDestroyRuntime()
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: rt,
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	done := make(chan error, 1)
	go func() {
		done <- srv.RunMaintenance(runCtx)
	}()

	select {
	case <-rt.destroyEntered:
	case <-time.After(5 * time.Second):
		close(rt.release)
		cancel()
		<-done
		t.Fatal("recovery stage never called Destroy")
	}

	// Many maintenance ticks elapse while the stage stays wedged; Destroy must
	// not be re-entered, or a hung stage would pile up goroutines every tick.
	time.Sleep(500 * time.Millisecond)
	rt.mu.Lock()
	calls := len(rt.destroyRuntimeIDs)
	rt.mu.Unlock()

	close(rt.release)
	cancel()
	<-done

	if calls != 1 {
		t.Fatalf("Destroy called %d times while wedged, want exactly 1 (stage must not overlap itself)", calls)
	}
}

// The runtime projection is the client's single source of truth for "is the
// agent busy, what is it doing, what is queued". These tests pin the contract at
// the HTTP boundary: the shape the client applies, and the two queue mutations
// that used to be client-side list edits.

func newRuntimeTestServer(t *testing.T, ctx context.Context, dir string) (*Server, *store.Store, store.Session) {
	t.Helper()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_runtime", string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{
		cfg:     testServerConfig(dir),
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)
	return srv, st, session
}

func runtimeRequest(t *testing.T, srv *Server, sessionID string) store.SessionRuntimeSnapshot {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/runtime", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, labUserID))
	rec := httptest.NewRecorder()
	srv.getSessionRuntime(rec, req, sessionID)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime endpoint: expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	var snapshot store.SessionRuntimeSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode runtime snapshot: %v", err)
	}
	return snapshot
}

// A send returns the turn id and the post-send projection — deliberately no
// message. The prompt is accepted but not yet asked: it becomes a transcript
// entry only when the agent starts it, so until then the queue owns it and the
// two can never show the same prompt at once.
func TestSendMessageResponseCarriesRuntimeProjection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"first"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		TurnID  int64                        `json:"turn_id"`
		Runtime store.SessionRuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if response.TurnID == 0 || response.Runtime.Revision == 0 {
		t.Fatalf("send response missing turn/revision: %+v", response)
	}
	if len(response.Runtime.QueuedTurns) != 1 || response.Runtime.QueuedTurns[0].ID != response.TurnID {
		t.Fatalf("projection does not show the new turn: %+v", response.Runtime)
	}
	waitForSessionStatus(t, ctx, st, session.ID, string(sessionstate.RunningActive))

	// No transcript entry yet: the prompt has not reached the agent. This is the
	// invariant that makes queue/transcript duplication impossible, and it is why
	// the eventual message timestamp reflects dispatch rather than typing.
	msgs, err := st.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("queued turn already in the transcript: %+v", msgs)
	}

	// The endpoint serves the identical shape, so a reload/second tab converges on
	// the same state without replaying events.
	fetched := runtimeRequest(t, srv, session.ID)
	if len(fetched.QueuedTurns) != 1 || fetched.QueuedTurns[0].ID != response.TurnID {
		t.Fatalf("endpoint disagrees with send response: %+v vs %+v", fetched, response.Runtime)
	}
	if fetched.Revision < response.Runtime.Revision {
		t.Fatalf("revision went backwards: %d -> %d", response.Runtime.Revision, fetched.Revision)
	}
}

// Withdrawing a queued turn is a server operation, not a client-side list edit.
// Removing the row only from the sender's view left the turn durable, so the
// prompt still executed once the worker got to it.
func TestCancelQueuedTurnRemovesItFromTheQueueAndTranscript(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)

	send := func(content string) int64 {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"`+content+`"}`))
		rec := httptest.NewRecorder()
		srv.sendMessage(rec, req, session.ID)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("send %q: expected 202, got %d body %s", content, rec.Code, rec.Body.String())
		}
		var response struct {
			TurnID int64 `json:"turn_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode send response: %v", err)
		}
		return response.TurnID
	}
	firstTurn := send("keep this")
	secondTurn := send("withdraw this")

	cancel := func(turnID int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/sessions/%s/turns/%d", session.ID, turnID), nil)
		req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, labUserID))
		rec := httptest.NewRecorder()
		srv.cancelQueuedTurn(rec, req, session.ID, turnID)
		return rec
	}

	rec := cancel(secondTurn)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel queued turn: expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	var snapshot store.SessionRuntimeSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	// The response is the same projection shape, so the client applies it exactly
	// as it applies a pushed snapshot.
	for _, turn := range snapshot.QueuedTurns {
		if turn.ID == secondTurn {
			t.Fatalf("withdrawn turn still queued: %+v", snapshot.QueuedTurns)
		}
	}
	var status string
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT status FROM turns WHERE id = ?`, secondTurn).Scan(&status); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if status != "canceled" {
		t.Fatalf("withdrawn turn status = %q, want canceled (it would still execute)", status)
	}
	// It was never asked, so it must not linger in history as an unanswered prompt.
	msgs, err := st.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, msg := range msgs {
		if msg.Content == "withdraw this" {
			t.Fatalf("withdrawn prompt left in transcript: %+v", msgs)
		}
	}

	// Racing the worker's claim reports a conflict rather than pretending to work.
	if _, err := st.DBForTest().ExecContext(ctx, `UPDATE turns SET status = 'running' WHERE id = ?`, firstTurn); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if rec := cancel(firstTurn); rec.Code != http.StatusConflict {
		t.Fatalf("cancel of a running turn: expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
}

// Stop ends the turn in flight and keeps what the user queued behind it, reporting
// how much is still pending so the UI can say the queue is resuming.
//
// The old behavior cancelled the queue, which did not match what "Stop" says: one
// press discarded prompts the user was still waiting on. It was safe rather than
// desired — a queued turn needs a live generation to be claimed, and Stop retires
// the one it had. Cold fallback now reconciles that, so the queue survives.
func TestInterruptSessionPreservesQueuedTurnsAndReportsDepth(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)

	var turnIDs []int64
	for _, content := range []string{"running work", "queued work"} {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"`+content+`"}`))
		rec := httptest.NewRecorder()
		srv.sendMessage(rec, req, session.ID)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("send %q: expected 202, got %d body %s", content, rec.Code, rec.Body.String())
		}
		var response struct {
			TurnID int64 `json:"turn_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode send response: %v", err)
		}
		turnIDs = append(turnIDs, response.TurnID)
	}
	waitForSessionStatus(t, ctx, st, session.ID, string(sessionstate.RunningActive))
	// No bridge runs in this fixture, so promote the head turn by hand to get the
	// realistic shape Stop faces: one turn executing, one queued behind it.
	if _, err := st.DBForTest().ExecContext(ctx,
		`UPDATE turns SET status = 'running', ack_started_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), turnIDs[0]); err != nil {
		t.Fatalf("mark head turn running: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/interrupt", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, labUserID))
	rec := httptest.NewRecorder()
	srv.interruptSession(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("interrupt: expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status        string                       `json:"status"`
		PendingQueued int64                        `json:"pending_queued"`
		Runtime       store.SessionRuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode interrupt response: %v", err)
	}
	if response.PendingQueued != 1 {
		t.Fatalf("pending_queued = %d, want 1", response.PendingQueued)
	}
	// The interrupted turn is gone; the follow-up the user queued is not.
	if response.Runtime.ActiveTurn != nil {
		t.Fatalf("the interrupted turn survived: %+v", response.Runtime.ActiveTurn)
	}
	// Still the user's prompt, and reported as parked: waiting on them, not on the
	// agent, so the UI can offer it back rather than implying it is about to run.
	if len(response.Runtime.QueuedTurns) != 1 || response.Runtime.QueuedTurns[0].ID != turnIDs[1] ||
		!response.Runtime.QueuedTurns[0].Parked {
		t.Fatalf("queue after interrupt = %+v, want just turn %d, parked", response.Runtime.QueuedTurns, turnIDs[1])
	}
	// Idle, because a parked queue is not pending work: nothing will run until the
	// user releases a row. Reporting running_active instead would pin the sandbox
	// for a queue that may never be released, and block checkpointing forever.
	if response.Status != string(sessionstate.RunningIdle) {
		t.Fatalf("status = %q, want %q", response.Status, sessionstate.RunningIdle)
	}
	var queuedStatus string
	var autoDispatch int
	if err := st.DBForTest().QueryRowContext(ctx,
		`SELECT status, auto_dispatch FROM turns WHERE id = ?`, turnIDs[1]).Scan(&queuedStatus, &autoDispatch); err != nil {
		t.Fatalf("query queued turn: %v", err)
	}
	if queuedStatus != "queued" || autoDispatch != 0 {
		t.Fatalf("queued turn = status %q auto_dispatch %d, want queued and parked", queuedStatus, autoDispatch)
	}
	// Cold fallback must NOT pick this up. It exists to allocate a replacement
	// generation for a queue that would otherwise strand — but Stop now leaves the
	// generation alive, and the queue parked on purpose. Starting a sandbox here
	// would resume the very work the user just stopped.
	fallbacks, err := st.ListColdFallbackSessions(ctx)
	if err != nil {
		t.Fatalf("list cold fallback sessions: %v", err)
	}
	for _, fallback := range fallbacks {
		if fallback.Session.ID == session.ID {
			t.Fatalf("cold fallback would resume a parked queue: %+v", fallback)
		}
	}
}

// TestSendMessageRuntimeFrameReachesSSEWire covers the server half of the
// contract that frontend/lib/use-event-stream.test.ts covers on the client:
// enqueuing a turn must put a `session.runtime` frame on the wire, framed with
// that exact event name.
//
// The name is the load-bearing part. EventSource routes a named frame only to a
// listener registered for that name, so if this type is ever renamed here
// without updating SSE_TYPED_EVENT_TYPES, every runtime push is dropped by the
// browser in silence — the failure that let a dispatched turn stay in the queue
// and execute a second time. Asserting on the serialized bytes (not just the hub
// event) is what makes this test able to catch that.
func TestSendMessageRuntimeFrameReachesSSEWire(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	session := createServerTestSession(t, ctx, st, dir, "sess_runtime_frame", string(sessionstate.Created), time.Now().UTC(), nil)

	srv := &Server{
		cfg:     testServerConfig(dir),
		store:   st,
		runtime: instantRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	ch, cancel := srv.hub.Subscribe(session.ID)
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"hello"}`))
	rec := httptest.NewRecorder()
	srv.sendMessage(rec, req, session.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body %s", rec.Code, rec.Body.String())
	}

	// Drain until the runtime projection shows up; sendMessage also publishes a
	// session status event, and their relative order is not part of the contract.
	var runtime *events.Event
	deadline := time.After(2 * time.Second)
	for runtime == nil {
		select {
		case event := <-ch:
			if event.Type == "session.runtime" {
				captured := event
				runtime = &captured
			}
		case <-deadline:
			t.Fatalf("no session.runtime event published after send")
		}
	}

	frame := httptest.NewRecorder()
	if err := srv.writeSSEEvent(frame, *runtime); err != nil {
		t.Fatalf("write sse event: %v", err)
	}
	wire := frame.Body.String()
	if !strings.Contains(wire, "event: session.runtime\n") {
		t.Fatalf("runtime frame is not named session.runtime on the wire:\n%s", wire)
	}

	// The payload must survive sanitization as a usable snapshot: the client
	// discards anything without a numeric revision, so a stripped field here
	// would look identical to a dropped frame.
	dataLine := ""
	for _, line := range strings.Split(wire, "\n") {
		if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if dataLine == "" {
		t.Fatalf("runtime frame carried no data line:\n%s", wire)
	}
	var delivered struct {
		Type    string `json:"type"`
		Payload struct {
			Revision    *int64 `json:"revision"`
			QueuedTurns []struct {
				ID      int64  `json:"id"`
				Content string `json:"content"`
			} `json:"queued_turns"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(dataLine), &delivered); err != nil {
		t.Fatalf("decode runtime frame: %v (%s)", err, dataLine)
	}
	if delivered.Type != "session.runtime" {
		t.Fatalf("delivered type = %q, want session.runtime", delivered.Type)
	}
	if delivered.Payload.Revision == nil {
		t.Fatalf("runtime frame lost its revision, client would discard it: %s", dataLine)
	}
	if len(delivered.Payload.QueuedTurns) != 1 || delivered.Payload.QueuedTurns[0].Content != "hello" {
		t.Fatalf("runtime frame did not carry the queued turn: %s", dataLine)
	}
}
