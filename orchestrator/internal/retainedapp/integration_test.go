//go:build khoraintegration

package retainedapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKhoraApplicationCompositionServesHTTPWithoutEmbeddedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	endpoint, template, manifestPath := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE"), os.Getenv("KHORA_INTEGRATION_MANIFEST")
	if endpoint == "" || template == "" || manifestPath == "" {
		t.Fatal("isolated engine/template/exported manifest required")
	}
	backend, err := khorabackend.New(ctx, khorabackend.Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := launchplan.Digest(raw)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := launchplan.Load(strings.NewReader(string(raw)), digest, map[string]execution.LaunchPlan{"sh": {TemplateID: template, CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := store.AcquireOwnerLock(filepath.Join(root, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
	app := server.New(config.Config{MaxSessions: 2, MaxEditBytes: 1 << 20, MaxUploadBytes: 1 << 20, MaxUploadFiles: 8}, st, nil, nil, events.NewHub(), slog.Default())
	flows := gateway.NewFlows()
	var reported sync.Map
	report := func(scope string, err error) {
		message := fmt.Sprintf("%s: %v", scope, err)
		if _, loaded := reported.LoadOrStore(message, true); !loaded {
			t.Log(message)
		}
	}
	runtime, err := Attach(ctx, app, st, Options{Backend: backend, Plans: plans, Routes: flows, Grants: flows, BackendName: "composition-engine", OwnerUUID: owner.UUID, NewSessionBackend: "khora", PreparedRoot: filepath.Join(root, "prepared"), MaxActive: 2, InventoryLimit: 10000, LeaseTTL: time.Minute, PollInterval: 50 * time.Millisecond, EventInterval: 50 * time.Millisecond, ActionTimeout: 5 * time.Second, CaptureWindow: time.Minute, InventoryInterval: 100 * time.Millisecond, OnError: report})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	background, stop := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() { finished <- runtime.Run(background) }()
	defer func() {
		stop()
		select {
		case err := <-finished:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(8 * time.Second):
			t.Error("composed services did not stop")
		}
	}()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		response := httptest.NewRecorder()
		app.Routes().ServeHTTP(response, req)
		return response
	}
	created := request(http.MethodPost, "/api/sessions", `{"mode":"shell"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("session creation: %d %s", created.Code, created.Body.String())
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil || session.ID == "" {
		t.Fatal("missing created session identity")
	}
	client, _ := sdk.New(endpoint)
	defer func() {
		runtime.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if environment, err := st.GetSessionEnvironment(cleanup, session.ID); err == nil {
			_ = client.Destroy(cleanup, environment.SandboxID)
		}
		for _, purpose := range []string{"workspace", "bridge"} {
			if binding, found, err := st.GetStorageBinding(cleanup, session.ID, purpose); err == nil && found {
				_ = client.DeleteVolume(cleanup, binding.VolumeID)
			}
		}
	}()
	upload := request(http.MethodPut, "/artifacts/"+session.ID+"/input.txt", "composed-runtime-ok\n")
	if upload.Code != http.StatusOK {
		t.Fatalf("pre-turn upload: %d %s", upload.Code, upload.Body.String())
	}
	sent := request(http.MethodPost, "/api/sessions/"+session.ID+"/messages", `{"content":"cat /workspace/input.txt; sleep 1; cp /workspace/input.txt /workspace/result.txt"}`)
	if sent.Code != http.StatusAccepted {
		t.Fatalf("HTTP turn: %d %s", sent.Code, sent.Body.String())
	}
	var turn struct {
		ID int64 `json:"turn_id"`
	}
	if err := json.Unmarshal(sent.Body.Bytes(), &turn); err != nil {
		t.Fatal(err)
	}
	for {
		status, reason, err := st.TurnStatus(ctx, session.ID, turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status == "running" {
			break
		}
		if status == "failed" {
			t.Fatal(reason)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if repaired, err := st.ReconcileStaleSessionStates(ctx, store.SessionRuntimeReconciliationParams{OwnerUUID: owner.UUID, Now: time.Now()}); err != nil || len(repaired) != 0 {
		t.Fatalf("legacy repair touched retained execution: %+v %v", repaired, err)
	}
	if renewed, err := st.RenewLiveGenerationLeases(ctx, store.RenewLiveGenerationsParams{Owner: store.GenerationLeaseOwner(owner.UUID), LeaseTTL: time.Minute, Now: time.Now()}); err != nil || renewed != 0 {
		t.Fatalf("legacy lease service owns retained execution: %d %v", renewed, err)
	}
	for {
		status, reason, err := st.TurnStatus(ctx, session.ID, turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			break
		}
		if status == "failed" {
			t.Fatal(reason)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	file := request(http.MethodGet, "/artifacts/"+session.ID+"/result.txt", "")
	body, _ := io.ReadAll(file.Result().Body)
	if file.Code != http.StatusOK || string(body) != "composed-runtime-ok\n" {
		t.Fatalf("composed workspace output: %d %q", file.Code, body)
	}
	binding, found, err := st.FindActiveExecutionBinding(ctx, session.ID)
	if err != nil || !found {
		t.Fatalf("composed execution binding: %+v %v", binding, err)
	}
	for st.AssertExecutionAccepted(ctx, binding.GenerationID, store.GenerationLeaseOwner(owner.UUID), time.Now()) != nil {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cursor, err := st.EngineCursor(ctx, binding.StoreID)
	if err != nil || cursor.After == 0 || cursor.Resync {
		t.Fatalf("composed event feed did not catch up: %+v %v", cursor, err)
	}
	if err := runtime.Controller.ReleaseIdle(ctx, session.ID, binding.GenerationID, binding.Owner); err != nil {
		t.Fatal(err)
	}
	for {
		var observed int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM engine_event_inbox i JOIN sandbox_environments e ON e.store_id=i.store_id AND e.sandbox_id=i.sandbox_id WHERE e.environment_id=? AND i.kind='paused' AND i.sequence>? AND e.observed_state='paused' AND NOT EXISTS(SELECT 1 FROM environment_reconcile_requests r WHERE r.environment_id=e.environment_id)`, binding.EnvironmentID, cursor.After).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if occupied, err := st.CountSandboxOccupyingSessions(ctx); err != nil || occupied != 0 {
		t.Fatalf("passive inspection did not settle paused capacity: %d %v", occupied, err)
	}
	for _, table := range []string{"network_profiles", "runtime_resource_instances", "session_driver_homes"} {
		var count int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("composition created legacy %s: %d %v", table, count, err)
		}
	}
}
