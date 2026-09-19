//go:build khoraintegration

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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/agentbridge"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/driverstate"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/nativecapture"
	capturesource "harness-platform/orchestrator/internal/nativecapture/guest"
	"harness-platform/orchestrator/internal/sandbox"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/sessionfiles"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/trajectory"
	"harness-platform/orchestrator/internal/workspace"
	guestfiles "harness-platform/orchestrator/internal/workspace/guest"
	"harness-platform/orchestrator/internal/workspace/prepared"
)

func TestKhoraExecutionWorkerCommitsTurnMessagesAndJournal(t *testing.T) { runKhoraWorker(t, "sh") }
func TestKhoraPiExecutionWorkerCommitsNativeState(t *testing.T)          { runKhoraWorker(t, "pi") }
func TestKhoraInterruptedRecoveryFlushesMissingCompletion(t *testing.T) {
	runKhoraWorker(t, "sh", true)
}
func runKhoraWorker(t *testing.T, driver string, truncated ...bool) {
	endpoint, template := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE")
	if endpoint == "" || template == "" {
		t.Fatal("isolated Khora endpoint/template required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	backend, err := khorabackend.New(ctx, khorabackend.Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := backend.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, directory)
	leaseOwner := store.GenerationLeaseOwner(owner.UUID)
	now := time.Now().UTC()
	session := store.Session{ExecutionBackend: "khora", ID: "khora-worker-" + driver + "-session", UserID: "lab", Agent: driver, Mode: store.ModeForDriver(driver), Status: "created", Workspace: "opaque-workspace", RestoreID: "unused", CreatedAt: now, UpdatedAt: now}
	if len(truncated) > 0 && truncated[0] {
		session.ID += "-truncated"
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	var mounts []sandbox.Mount
	var volumes []string
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if environment, err := st.GetSessionEnvironment(cleanup, session.ID); err == nil {
			_ = client.Destroy(cleanup, environment.SandboxID)
		}
		for _, purpose := range []string{"workspace", "bridge"} {
			if binding, found, err := st.GetStorageBinding(cleanup, session.ID, purpose); err == nil && found {
				_ = client.DeleteVolume(cleanup, binding.VolumeID)
			}
		}
		for _, volume := range volumes {
			_ = client.DeleteVolume(cleanup, volume)
		}
	})
	var network sandbox.Network
	var blockModel atomic.Bool
	var adoptionModelHold atomic.Pointer[chan struct{}]
	adoptionModelEntered := make(chan struct{}, 1)
	modelEntered := make(chan struct{}, 1)
	var processEnvironment []string
	if driver == "pi" {
		listener, err := net.Listen("tcp4", "192.0.2.1:0")
		if err != nil {
			t.Fatal(err)
		}
		modelServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				w.WriteHeader(200)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			frames := []string{
				`{"type":"message_start","message":{"id":"synthetic-message","type":"message","role":"assistant","model":"synthetic-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"product-worker-ok"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`,
				`{"type":"message_stop"}`,
			}
			for _, frame := range frames {
				var event struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal([]byte(frame), &event)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, frame)
				if event.Type == "message_start" {
					if hold := adoptionModelHold.Load(); hold != nil {
						w.(http.Flusher).Flush()
						select {
						case adoptionModelEntered <- struct{}{}:
						default:
						}
						select {
						case <-*hold:
						case <-r.Context().Done():
							return
						}
					}
				}
				if event.Type == "message_start" && blockModel.Load() {
					w.(http.Flusher).Flush()
					select {
					case modelEntered <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return
				}
			}
		})}
		go modelServer.Serve(listener)
		defer modelServer.Close()
		address := listener.Addr().(*net.TCPAddr).AddrPort()
		modelURL := "http://" + address.String()
		network.Allow = []sandbox.Tuple{{CIDR: netip.PrefixFrom(address.Addr(), 32), Port: address.Port(), Protocol: "tcp"}}
		configVolume, err := backend.CreateVolume(ctx, "worker-pi-config", 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		volumes = append(volumes, configVolume.VolumeID)
		if _, err := client.MkdirVolume(ctx, configVolume.VolumeID, "pi", 0755); err != nil {
			t.Fatal(err)
		}
		models := map[string]any{"providers": map[string]any{"harness_anthropic_proxy": map[string]any{"baseUrl": modelURL, "api": "anthropic-messages", "apiKey": "harness-placeholder", "models": []map[string]any{{"id": "synthetic-model", "name": "synthetic-model", "reasoning": false, "input": []string{"text"}, "contextWindow": 8192, "maxTokens": 256, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}}}
		encoded, _ := json.Marshal(models)
		for name, body := range map[string][]byte{"pi/models.json": encoded, "pi/settings.json": []byte(`{"compaction":{"enabled":false}}`)} {
			if _, err := client.WriteVolume(ctx, configVolume.VolumeID, name, 0, body); err != nil {
				t.Fatal(err)
			}
		}
		mounts = append(mounts, sandbox.Mount{VolumeID: configVolume.VolumeID, Destination: "/harness-config", Access: "shared-ro"})
		processEnvironment = []string{"PI_MODEL=synthetic-model", "ANTHROPIC_BASE_URL=" + modelURL}
	}
	srv := &Server{store: st, hub: events.NewHub(), log: slog.Default(), cfg: testServerConfig(directory)}
	srv.cfg.MaxEditBytes = 1 << 20
	srv.SetWorkspaceReader(sessionfiles.Reader{Repository: st, Backend: backend})
	configureInventory := func(owner string) {
		srv.SetWorkspaceInventory(sessionfiles.Scanner{Repository: st, Source: sessionfiles.Reader{Repository: st, Backend: backend}, Owner: owner, Limit: 10000, AfterCommit: srv.publishDurableEvent})
	}
	configureInventory(leaseOwner)
	preparedUploads, err := prepared.New(filepath.Join(directory, "prepared-uploads"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetWorkspaceWriter(&sessionfiles.Writer{Repository: st, Backend: backend, Lifecycle: backend, MaxActive: 2, Content: preparedUploads})
	srv.nativeStateValidator = func(ctx context.Context, input store.DriverStateValidationInput, turn int64) (workspace.File, error) {
		return driverstate.ValidateRemote(ctx, input.CanonicalPayload, fmt.Sprint(turn), "native-state-"+driver+"-"+fmt.Sprint(turn), guestfiles.Files{Backend: backend, Reference: input.Reference, Root: "/root/.pi/agent/sessions"})
	}
	var interruptedGeneration atomic.Pointer[string]
	var invalidateFirstTurn atomic.Bool
	invalidateFirstTurn.Store(true)
	invalidationResult := make(chan error, 1)
	interrupted, detached := make(chan struct{}, 1), make(chan struct{}, 1)
	controlBackend := &partialAbortBackend{Processes: backend}
	launcher := agentbridge.Launcher{Backend: controlBackend, Store: store.BoundBridgeStore{Store: st}, Receipts: st, Commands: st, IdleReplay: st, ActiveReplay: st, Requests: st, LeaseTTL: time.Minute, BeforeComplete: srv.projectBeforeBridgeCompletion, AfterEventCommit: srv.publishDurableEvent}
	launcher.AfterCommit = func(ctx context.Context, envelope bridge.Envelope, event int64) {
		srv.handleBridgeCommittedEnvelope(ctx, envelope, event)
		if envelope.Type == bridge.TypeAckTurnStarted && invalidateFirstTurn.CompareAndSwap(true, false) {
			// Deliver actual retained engine history after the guest has begun its
			// first turn. Output and native completion must settle behind that fence.
			cursor, err := st.EngineCursor(ctx, identity.StoreID)
			if err == nil {
				var page sandbox.EventPage
				page, err = backend.Events(ctx, cursor.After, 128)
				if err == nil {
					_, err = st.CommitEngineEvents(ctx, identity.StoreID, cursor.After, page.Events)
				}
			}
			invalidationResult <- err
		}
		if target := interruptedGeneration.Load(); target != nil && *target == envelope.GenerationID && envelope.Type == bridge.TypeAckTurnStarted {
			interrupted <- struct{}{}
			<-ctx.Done()
		}
	}
	launcher.OnExit = func(binding execution.Binding, _ error) {
		if target := interruptedGeneration.Load(); target != nil && *target == binding.GenerationID {
			detached <- struct{}{}
		}
	}
	manifest, err := os.ReadFile(os.Getenv("KHORA_INTEGRATION_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := launchplan.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := launchplan.Load(strings.NewReader(string(manifest)), manifestDigest, map[string]execution.LaunchPlan{driver: {TemplateID: template, CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20, Mounts: mounts, Network: network, Environment: processEnvironment}})
	if err != nil {
		t.Fatal(err)
	}
	repository := &acceptanceFaultRepository{Store: st, fail: driver == "sh"}
	controller := &execution.Controller{Repository: repository, Backend: backend, Agents: launcher, Plans: plans, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
	defer func() { controller.Close() }()
	if driver == "sh" {
		srv.ownerUUID = owner.UUID
		srv.SetWorkspaceWriter(&sessionfiles.Writer{Repository: st, Backend: backend, Lifecycle: backend, Content: preparedUploads, MaxActive: 2, Provisioner: controller})
		request := httptest.NewRequest(http.MethodPut, "/artifacts/"+session.ID+"/before-first-turn.txt", strings.NewReader("product-worker-ok\n")).WithContext(context.WithValue(ctx, userContextKey{}, session.UserID))
		response := httptest.NewRecorder()
		srv.writeArtifact(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("new-session workspace upload: %d %s", response.Code, response.Body.String())
		}
		var executions int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM execution_bindings WHERE session_id=?`, session.ID).Scan(&executions); err != nil || executions != 0 {
			t.Fatalf("first upload launched an agent: %d %v", executions, err)
		}
		assertIntegrationCapacity(t, ctx, st, 0)
	}
	if driver == "sh" {
		if _, err := controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner); err == nil {
			t.Fatal("injected acceptance failure ignored")
		}
	}
	if err := srv.SetExecutionController(controller, "khora"); err != nil {
		t.Fatal(err)
	}
	ensured, err := srv.ensureActiveGeneration(ctx, session, leaseOwner)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.startEnsuredGeneration(ctx, session, ensured, startFailureInputAcceptable); err != nil {
		t.Fatal(err)
	}
	binding, err := st.GetExecutionBinding(ctx, ensured.Allocation.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	initial := binding
	var initialNativeID, initialNativeFile string
	for cycle := 0; cycle < 3; cycle++ {
		if cycle == 1 {
			latest, err := st.GetSession(ctx, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := srv.releaseScheduledIdle(ctx, latest, leaseOwner); err != nil {
				t.Fatal(err)
			}
			assertIntegrationCapacity(t, ctx, st, 0)
			if driver == "sh" {
				verifyKhoraWorkspaceDownload(t, ctx, srv, st, controller, session.ID, leaseOwner, true)
			}
			// Replay the actual historical pause before the replacement starts.
			// Its invalidation must be settled by new epoch readiness, not ignored.
			for paused := false; !paused; {
				cursor, err := st.EngineCursor(ctx, identity.StoreID)
				if err != nil {
					t.Fatal(err)
				}
				page, err := backend.Events(ctx, cursor.After, 128)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.CommitEngineEvents(ctx, identity.StoreID, cursor.After, page.Events); err != nil {
					t.Fatal(err)
				}
				for _, event := range page.Events {
					if event.SandboxID == binding.SandboxID && event.Kind == "paused" {
						paused = true
					}
				}
				if !paused {
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
			assertIntegrationCapacity(t, ctx, st, 1)
			if _, err := execution.InspectEnvironment(ctx, st, backend, binding.EnvironmentID); err != nil {
				t.Fatal(err)
			}
			assertIntegrationCapacity(t, ctx, st, 0)
			binding, err = controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner)
			if err != nil {
				t.Fatal(err)
			}
			if binding.SandboxID != initial.SandboxID || binding.Epoch != initial.Epoch+1 || binding.GenerationID == initial.GenerationID {
				t.Fatalf("retained resume lost identity: %+v -> %+v", initial, binding)
			}
			assertIntegrationCapacity(t, ctx, st, 1)
		}
		if cycle == 2 {
			previous := binding
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			watermark, err := st.BridgeStreamWatermark(ctx, previous.StoreID, previous.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			probe := fmt.Sprintf(`import json,sqlite3,time
db=sqlite3.connect('file:/harness-bridge/%s/journal.db?mode=ro',uri=True)
for attempt in range(300):
 records=[json.loads(bytes(row[0])[1:]) for row in db.execute('SELECT payload FROM records WHERE sequence>?',(%d,)).fetchall()]
 if any(r.get('envelope',{}).get('type')=='claim_next_turn' for r in records):break
 time.sleep(0.02)
else:raise RuntimeError('detached guest did not journal its next claim')
`, previous.GenerationID, watermark)
			runHistoricalRecoveryProbe(t, ctx, backend, previous, "inspect-idle-request", probe)
			replacement, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "replacement-owner"))
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Close()
			if err := st.WriteOwner(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			leaseOwner = store.GenerationLeaseOwner(replacement.UUID)
			configureInventory(leaseOwner)
			controller = &execution.Controller{Repository: &acceptanceFaultRepository{Store: st, fail: true}, Backend: backend, Agents: launcher, Plans: plans, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
			starts := controlBackend.starts.Load()
			writer, ok := srv.workspaceWrites.(execution.WorkspaceRecovery)
			if !ok {
				t.Fatal("reconciliation writer unavailable")
			}
			stopAdoption := startKhoraReconciler(t, ctx, srv, st, controller, writer, leaseOwner)
			for {
				binding, err = st.GetExecutionBinding(ctx, previous.GenerationID)
				if err != nil {
					t.Fatal(err)
				}
				if binding.Owner == leaseOwner && binding.State == "accepted" {
					var delivered int
					if err := st.DBForTest().QueryRow(`SELECT count(*) FROM deferred_turn_requests WHERE generation_id=? AND state='delivered'`, previous.GenerationID).Scan(&delivered); err != nil {
						t.Fatal(err)
					}
					if delivered > 0 {
						break
					}
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			stopAdoption()
			if controlBackend.starts.Load() != starts || controlBackend.connects.Load() < 2 {
				t.Fatal("idle adoption retry started a new process instead of reconnecting")
			}
			if binding.SandboxID != previous.SandboxID || binding.Epoch != previous.Epoch || binding.GenerationID != previous.GenerationID || binding.ProcessHandle != previous.ProcessHandle || binding.Owner != leaseOwner {
				t.Fatalf("idle owner recovery lost retained environment: %+v", binding)
			}
		}
		content := "printf 'product-worker-ok\\n'"
		if driver == "sh" {
			if cycle == 0 {
				content = "cp /workspace/before-first-turn.txt /workspace/retained-controller-proof; ln -s /workspace /workspace/symlink-parent; python3 -c 'from pathlib import Path; p=Path(\"/workspace/pages\"); p.mkdir(); [(p/f\"file-{i:02d}\").write_text(\"page\") for i in range(40)]'; cat /workspace/retained-controller-proof"
			} else {
				content = "cat /workspace/retained-controller-proof"
			}
		}
		if err := srv.SetExecutionController(controller, "khora"); err != nil {
			t.Fatal(err)
		}
		srv.ownerUUID, _, _ = strings.Cut(leaseOwner, ":")
		body, _ := json.Marshal(map[string]string{"content": content})
		request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.ID+"/messages", strings.NewReader(string(body))).WithContext(context.WithValue(ctx, userContextKey{}, session.UserID))
		response := httptest.NewRecorder()
		srv.sendMessage(response, request, session.ID)
		if response.Code != http.StatusAccepted {
			t.Fatalf("Khora HTTP dispatch: %d %s", response.Code, response.Body.String())
		}
		var enqueued struct {
			TurnID int64 `json:"turn_id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &enqueued); err != nil || enqueued.TurnID == 0 {
			t.Fatalf("HTTP turn identity: %+v %v", enqueued, err)
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			status, reason, err := st.TurnStatus(ctx, session.ID, enqueued.TurnID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "completed" {
				break
			}
			if status == "failed" || status == "canceled" {
				t.Fatalf("worker turn ended %s: %s", status, reason)
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		messages, err := st.ListMessages(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range messages {
			if message.Role == "assistant" && message.TurnID != nil && *message.TurnID == enqueued.TurnID && strings.Contains(message.Content, "product-worker-ok") {
				found = true
			}
		}
		if !found {
			t.Fatalf("durable transcript lost native output: %+v", messages)
		}
		watermark, err := st.BridgeStreamWatermark(ctx, identity.StoreID, binding.GenerationID)
		if err != nil || watermark == 0 {
			t.Fatalf("journal watermark: %d %v", watermark, err)
		}
		for _, table := range []string{"network_profiles", "runtime_resource_instances", "active_model_request_contexts"} {
			var count int
			if err := st.DBForTest().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("legacy %s populated: %d %v", table, count, err)
			}
		}
		if _, err := os.Stat(filepath.Join(directory, "sessions", session.ID)); err == nil {
			t.Fatal("opaque execution provisioned a host workspace")
		}
		if driver == "pi" {
			var version int
			var payload string
			if err := st.DBForTest().QueryRow(`SELECT state_version,state_payload FROM session_driver_states WHERE session_id=? AND driver_id='pi'`, session.ID).Scan(&version, &payload); err != nil {
				t.Fatal(err)
			}
			if version != cycle+2 || !strings.Contains(payload, "/root/.pi/agent/sessions") || !strings.Contains(payload, "selected_session_id") {
				t.Fatalf("native Pi state not committed: version=%d payload=%s", version, payload)
			}
			var native struct {
				ID   string `json:"selected_session_id"`
				File string `json:"selected_session_file"`
			}
			if err := json.Unmarshal([]byte(payload), &native); err != nil {
				t.Fatal(err)
			}
			if cycle == 0 {
				initialNativeID, initialNativeFile = native.ID, native.File
			} else if native.ID != initialNativeID || native.File != initialNativeFile {
				t.Fatalf("cold resume selected another native history: %+v", native)
			}
			var snapshots int
			if err := st.DBForTest().QueryRow(`SELECT count(*) FROM native_capture_requests WHERE session_id=? AND native_id=?`, session.ID, native.ID).Scan(&snapshots); err != nil || snapshots != cycle+1 {
				t.Fatalf("native snapshots not committed with state: %d %v", snapshots, err)
			}
		}
		t.Logf("accepted execution=%s epoch=%d turn=%d messages=%d journal_watermark=%d", binding.GenerationID, binding.Epoch, enqueued.TurnID, len(messages), watermark)
		if cycle == 0 {
			select {
			case err := <-invalidationResult:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if sequence, err := st.EnvironmentInvalidationSequence(ctx, binding.EnvironmentID); err != nil || sequence < 0 {
				t.Fatalf("first turn did not settle behind engine invalidation: %d %v", sequence, err)
			}
			if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, leaseOwner, time.Now()); err == nil {
				t.Fatal("turn settlement restored readiness without process reconciliation")
			}
			starts := controlBackend.starts.Load()
			previous := binding
			binding, err = controller.ReconnectIdle(ctx, session.ID, binding.GenerationID, leaseOwner)
			if err != nil {
				t.Fatal(err)
			}
			if controlBackend.starts.Load() != starts || binding.Epoch != previous.Epoch || binding.ProcessHandle != previous.ProcessHandle {
				t.Fatal("post-settlement reconciliation replaced the original process")
			}
		}
		if driver == "sh" && cycle == 0 {
			verifyKhoraWorkspaceDownload(t, ctx, srv, st, controller, session.ID, leaseOwner, false)
			verifyKhoraWorkspaceWrite(t, ctx, srv, st, controller, backend, preparedUploads, session.ID, leaseOwner)
			verifyKhoraInventory(t, ctx, srv, st, session.ID, false)
		}
	}
	controller, binding, leaseOwner = verifyKhoraActiveAdoption(t, ctx, srv, st, controller, backend, controlBackend, launcher, plans, binding, leaseOwner, &adoptionModelHold, adoptionModelEntered, configureInventory)
	binding = verifyKhoraDeadIdleRecovery(t, ctx, srv, st, controller, backend, controlBackend, binding, leaseOwner)
	if driver == "sh" {
		controlBackend.fail.Store(true)
		verifyKhoraShellStop(t, ctx, srv, st, controller, binding, leaseOwner)
	} else {
		blockModel.Store(true)
		controlBackend.failSteer.Store(true)
		verifyKhoraPiControls(t, ctx, srv, st, controller, binding, leaseOwner, modelEntered)
		blockModel.Store(false)
	}
	{
		var completedNativeVersion int
		if driver == "pi" {
			if err := st.DBForTest().QueryRow(`SELECT state_version FROM session_driver_states WHERE session_id=? AND driver_id='pi'`, session.ID).Scan(&completedNativeVersion); err != nil {
				t.Fatal(err)
			}
		}
		target := binding.GenerationID
		interruptedGeneration.Store(&target)
		content := `python3 -c 'print("historical-recovery-ok" + "x" * 262144)'`
		terminal := "ack_turn_completed"
		if len(truncated) > 0 && truncated[0] {
			content = `python3 -c 'import time; print("historical-recovery-ok" + "x" * 262144, flush=True); time.sleep(60)'`
			terminal = "emit_output"
		}
		turn, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: session.ID, Content: content})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-interrupted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		probe := fmt.Sprintf(`import json,sqlite3,time
db=sqlite3.connect('file:/harness-bridge/%s/journal.db?mode=ro',uri=True)
for attempt in range(300):
 records=[json.loads(bytes(row[0])[1:]) for row in db.execute('SELECT payload FROM records').fetchall()]
 if any(r.get('envelope',{}).get('type')=='%s' and r['envelope'].get('turn_id')==%d for r in records):break
 time.sleep(0.02)
else:raise RuntimeError('guest did not commit terminal journal record')
`, binding.GenerationID, terminal, turn.TurnID)
		runHistoricalRecoveryProbe(t, ctx, backend, binding, "inspect-retained-journal", probe)
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-detached:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		assertIntegrationCapacity(t, ctx, st, 1)
		faults := &recoveryFaultRecords{Store: st, fail: true}
		reader := &recoveryFaultReader{Reader: agentbridge.RecoveryReader{Backend: backend, Records: faults}, fail: true}
		nativeRestorer := &nativeFaultRestorer{Native: driverstate.Restorer{Backend: backend}, fail: driver == "pi"}
		recovery := &execution.Controller{Repository: st, Backend: backend, Routes: gateway.NewFlows(), RecoveryReader: reader, RecoveryProjector: srv, NativeRestorer: nativeRestorer, Lifetime: ctx}
		defer recovery.Close()
		const reason = "injected transport loss before output ingestion"
		failures := []string{"lost ingest reply", "lost stopped-reader reply"}
		if driver == "pi" {
			failures = append(failures, "lost native restore reply")
		}
		for _, failure := range failures {
			if err := recovery.RecoverInterrupted(ctx, session.ID, binding.GenerationID, leaseOwner, reason); err == nil {
				t.Fatalf("%s injection ignored", failure)
			}
			status, _, err := st.TurnStatus(ctx, session.ID, turn.TurnID)
			if err != nil || status != "running" {
				t.Fatalf("interrupted turn classified before recovered output: %s %v", status, err)
			}
			assertIntegrationCapacity(t, ctx, st, 1)
		}
		if err := recovery.RecoverInterrupted(ctx, session.ID, binding.GenerationID, leaseOwner, reason); err != nil {
			t.Fatal(err)
		}
		if err := recovery.RecoverInterrupted(ctx, session.ID, binding.GenerationID, leaseOwner, reason); err != nil {
			t.Fatal(err)
		}
		assertIntegrationCapacity(t, ctx, st, 0)
		head, err := st.BridgeStreamWatermark(ctx, binding.StoreID, binding.GenerationID)
		if err != nil {
			t.Fatal(err)
		}
		messages, err := st.ListMessages(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		needle := "historical-recovery-ok"
		if driver == "pi" {
			needle = "product-worker-ok"
		}
		found := 0
		for _, message := range messages {
			if message.TurnID != nil && *message.TurnID == turn.TurnID && message.Role == "assistant" && strings.Contains(message.Content, needle) {
				found++
			}
		}
		if found != 1 {
			rows, _ := st.DBForTest().Query(`SELECT type,substr(payload,1,1500) FROM bridge_ingress WHERE generation_id=? AND turn_id=? ORDER BY id`, binding.GenerationID, turn.TurnID)
			if rows != nil {
				for rows.Next() {
					var kind, payload string
					rows.Scan(&kind, &payload)
					t.Logf("recovered %s: %s", kind, payload)
				}
				rows.Close()
			}
			t.Fatalf("historical journal recovery duplicated or lost interrupted output: %d", found)
		}
		var bytes int
		if err := st.DBForTest().QueryRow(`SELECT coalesce(sum(length(payload)),0) FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND type='emit_output'`, binding.GenerationID, turn.TurnID).Scan(&bytes); err != nil || (driver == "sh" && bytes < 262144) {
			t.Fatalf("recovery accepted only a process tail: %d %v", bytes, err)
		}
		status, _, err := st.TurnStatus(ctx, session.ID, turn.TurnID)
		if err != nil || status != "failed" {
			t.Fatalf("historical completion changed turn classification: %s %v", status, err)
		}
		if driver == "pi" {
			var digest, path string
			var version int
			if err := st.DBForTest().QueryRow(`SELECT c.native_digest,c.selected_path,d.state_version FROM native_capture_requests c JOIN session_driver_states d ON d.session_id=c.session_id AND d.state_version=c.state_version WHERE c.session_id=?`, session.ID).Scan(&digest, &path, &version); err != nil || version != completedNativeVersion {
				t.Fatalf("failed native turn advanced state: %d %v", version, err)
			}
			controller = &execution.Controller{Repository: st, Backend: backend, Agents: launcher, Plans: plans, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
			successor, err := controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner)
			if err != nil {
				t.Fatal(err)
			}
			files := guestfiles.Files{Backend: backend, Reference: sandbox.Reference{StoreID: successor.StoreID, SandboxID: successor.SandboxID, Epoch: successor.Epoch}, Root: "/root/.pi/agent/sessions"}
			restored, err := files.SyncNative(ctx, "verify-restored-native", path, initialNativeID)
			if err != nil || restored.Digest != digest {
				t.Fatalf("native recovery did not restore completed bytes: %+v %v", restored, err)
			}
			archive := fmt.Sprintf(".harness-completed/interrupted-native-restore-%x.jsonl", sha256.Sum256([]byte("interrupt-native-restore:"+binding.GenerationID)))
			interrupted, err := files.SyncNative(ctx, "verify-interrupted-native", archive, initialNativeID)
			if err != nil || interrupted.Digest == digest {
				t.Fatalf("interrupted native bytes not retained separately: %+v %v", interrupted, err)
			}
			next, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: session.ID, Content: "continue after explicit recovery"})
			if err != nil {
				t.Fatal(err)
			}
			for {
				status, reason, err := st.TurnStatus(ctx, session.ID, next.TurnID)
				if err != nil {
					t.Fatal(err)
				}
				if status == "completed" {
					break
				}
				if status == "failed" || status == "canceled" {
					t.Fatalf("successor turn: %s %s", status, reason)
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			var advanced int
			if err := st.DBForTest().QueryRow(`SELECT state_version FROM session_driver_states WHERE session_id=?`, session.ID).Scan(&advanced); err != nil || advanced != completedNativeVersion+1 {
				t.Fatalf("successor native state advancement: %d %v", advanced, err)
			}
			mem, err := memorystore.Open(ctx, filepath.Join(directory, "capture-memory.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer mem.Close()
			captureStore := &captureFaultRepository{Store: st, fail: true}
			capture := nativecapture.Worker{Repository: captureStore, Source: capturesource.Source{Backend: backend}, Sink: trajectory.NewCapturer(mem, nil, filepath.Join(directory, "trajectory"), nil)}
			if err := controller.ReleaseIdle(ctx, session.ID, successor.GenerationID, leaseOwner); err != nil {
				t.Fatal(err)
			}
			if n, err := capture.Step(ctx, 32); err != nil || n != 0 {
				t.Fatalf("paused capture was not deferred: %d %v", n, err)
			}
			writer, ok := srv.workspaceWrites.(execution.WorkspaceRecovery)
			if !ok {
				t.Fatal("workspace recovery missing")
			}
			stopCaptureMaintenance := startKhoraReconciler(t, ctx, srv, st, controller, writer, leaseOwner)
			waitCaptureMaintenance(t, ctx, st, session.ID, "reading")
			var active int
			if err := st.DBForTest().QueryRow(`SELECT count(*) FROM execution_bindings WHERE session_id=? AND state!='retired'`, session.ID).Scan(&active); err != nil || active != 0 {
				t.Fatalf("capture maintenance started agent: %d %v", active, err)
			}
			assertIntegrationCapacity(t, ctx, st, 1)
			if n, err := capture.Step(ctx, 32); err == nil || n != 0 {
				t.Fatalf("lost capture receipt not surfaced: %d %v", n, err)
			}
			for range completedNativeVersion {
				if n, err := capture.Step(ctx, 32); err != nil || n != 1 {
					t.Fatalf("completed snapshot capture: %d %v", n, err)
				}
			}
			if n, err := capture.Step(ctx, 32); err != nil || n != 0 {
				t.Fatalf("capture replay duplicated work: %d %v", n, err)
			}
			waitCaptureMaintenance(t, ctx, st, session.ID, "complete")
			stopCaptureMaintenance()
			assertIntegrationCapacity(t, ctx, st, 0)
			rows, err := st.DBForTest().Query(`SELECT r.native_digest,c.archive_path FROM native_capture_requests r JOIN native_capture_receipts c USING(session_id,state_version) WHERE r.session_id=?`, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var digest, path string
				if err := rows.Scan(&digest, &path); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != digest {
					t.Fatalf("consumer archive differs from completed source: %s %v", path, err)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			var archives int
			if err := mem.DBForTest().QueryRow(`SELECT count(*) FROM raw_sources WHERE session_id=?`, session.ID).Scan(&archives); err != nil || archives != completedNativeVersion {
				t.Fatalf("capture retry changed consumer archive count: %d %v", archives, err)
			}
		}
		t.Logf("historical recovery origin=%s epoch=%d head=%d preserved failed turn=%d", binding.GenerationID, binding.Epoch, head, turn.TurnID)
		if driver == "sh" {
			verifyKhoraPausedWrite(t, ctx, srv, st, backend, preparedUploads, session.ID, leaseOwner)
			verifyKhoraInventory(t, ctx, srv, st, session.ID, true)
			controller = &execution.Controller{Repository: st, Backend: backend, Agents: launcher, Plans: plans, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
			if _, err := controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner); err != nil {
				t.Fatalf("normal session resume after paused upload: %v", err)
			}
		}
	}

}

func verifyKhoraActiveAdoption(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, backend *khorabackend.Backend, processes *partialAbortBackend, launcher agentbridge.Launcher, plans execution.PlanSource, binding execution.Binding, owner string, modelHold *atomic.Pointer[chan struct{}], modelEntered <-chan struct{}, inventory func(string)) (*execution.Controller, execution.Binding, string) {
	t.Helper()
	for cycle := 0; cycle < 3; cycle++ {
		previous := binding
		if cycle == 2 {
			processes.failGrant.Store(true)
		}
		hold := make(chan struct{})
		if binding.Driver == "pi" {
			modelHold.Store(&hold)
		}
		content := fmt.Sprintf("printf 'before-active-adoption\\n'; while [ ! -f /workspace/active-adoption-%d ]; do sleep 0.05; done; printf 'product-worker-ok\\n'", cycle)
		turn, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: content})
		if err != nil {
			t.Fatal(err)
		}
		if binding.Driver == "pi" && cycle != 2 {
			select {
			case <-modelEntered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		for {
			status, reason, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
			if err != nil || status == "failed" || status == "canceled" {
				t.Fatalf("active adoption input: %s %s %v", status, reason, err)
			}
			if (cycle != 2 && status == "running") || (cycle == 2 && status == "leased" && !processes.failGrant.Load()) {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		release := func() {
			if binding.Driver == "pi" {
				modelHold.Store(nil)
				close(hold)
			} else {
				runHistoricalRecoveryProbe(t, ctx, backend, binding, fmt.Sprintf("release-active-%d", cycle), fmt.Sprintf("from pathlib import Path; Path('/workspace/active-adoption-%d').write_text('go')", cycle))
			}
		}
		if cycle == 1 {
			release()
			probe := fmt.Sprintf(`import json,sqlite3,time
 db=sqlite3.connect('file:/harness-bridge/%s/journal.db?mode=ro',uri=True)
 for attempt in range(300):
  records=[json.loads(bytes(row[0])[1:]) for row in db.execute('SELECT payload FROM records').fetchall()]
  if any(r.get('envelope',{}).get('type')=='ack_turn_completed' and r['envelope'].get('turn_id')==%d for r in records):break
  time.sleep(0.02)
 else:raise RuntimeError('detached active turn did not journal completion')
 `, binding.GenerationID, turn.TurnID)
			probe = strings.ReplaceAll(probe, "\n ", "\n")
			runHistoricalRecoveryProbe(t, ctx, backend, binding, "inspect-active-completion", probe)
		}
		if cycle == 0 {
			cursor, err := st.EngineCursor(ctx, binding.StoreID)
			if err != nil {
				t.Fatal(err)
			}
			page, err := backend.Events(ctx, cursor.After, 128)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CommitEngineEvents(ctx, binding.StoreID, cursor.After, page.Events); err != nil {
				t.Fatal(err)
			}
			if sequence, err := st.EnvironmentInvalidationSequence(ctx, binding.EnvironmentID); err != nil || sequence < 0 {
				t.Fatalf("active revalidation lacked an engine event: %d %v", sequence, err)
			}
		} else {
			replacementOwner, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "active-owner"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = replacementOwner.Close() })
			if err := st.WriteOwner(ctx, replacementOwner); err != nil {
				t.Fatal(err)
			}
			owner = store.GenerationLeaseOwner(replacementOwner.UUID)
			srv.ownerUUID = replacementOwner.UUID
		}
		inventory(owner)
		controller = &execution.Controller{Repository: &acceptanceFaultRepository{Store: st, fail: true}, Backend: backend, Agents: launcher, Plans: plans, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
		if err := srv.SetExecutionController(controller, "khora"); err != nil {
			t.Fatal(err)
		}
		starts, connects := processes.starts.Load(), processes.connects.Load()
		writer, ok := srv.workspaceWrites.(execution.WorkspaceRecovery)
		if !ok {
			t.Fatal("active adoption recovery writer unavailable")
		}
		stop := startKhoraReconciler(t, ctx, srv, st, controller, writer, owner)
		for {
			binding, err = st.GetExecutionBinding(ctx, previous.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			if binding.Owner == owner && binding.State == "accepted" {
				var adopted int
				if err := st.DBForTest().QueryRow(`SELECT count(*) FROM active_execution_adoptions WHERE generation_id=? AND turn_id=? AND owner=? AND state='accepted'`, binding.GenerationID, turn.TurnID, owner).Scan(&adopted); err != nil {
					t.Fatal(err)
				}
				if adopted == 1 {
					break
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		stop()
		if binding.Epoch != previous.Epoch || binding.ProcessHandle != previous.ProcessHandle || binding.SandboxID != previous.SandboxID || processes.starts.Load() != starts || processes.connects.Load() < connects+2 {
			t.Fatalf("active adoption replaced original process: %+v -> %+v", previous, binding)
		}
		if cycle != 1 {
			release()
		}
		for {
			status, reason, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
			if err != nil || status == "failed" || status == "canceled" {
				t.Fatalf("adopted active turn: %s %s %v", status, reason, err)
			}
			if status == "completed" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		var attempt, messages int
		if err := st.DBForTest().QueryRow(`SELECT attempt FROM turns WHERE id=? AND generation_id=?`, turn.TurnID, previous.GenerationID).Scan(&attempt); err != nil || attempt != 1 {
			t.Fatalf("active reconnect redispatched turn: %d %v", attempt, err)
		}
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='assistant' AND content LIKE '%product-worker-ok%'`, turn.TurnID).Scan(&messages); err != nil || messages != 1 {
			t.Fatalf("active reconnect duplicated or lost transcript: %d %v", messages, err)
		}
		t.Logf("active same-epoch adoption driver=%s turn=%d completed_while_detached=%t lost_grant_reply=%t", binding.Driver, turn.TurnID, cycle == 1, cycle == 2)
	}
	return controller, binding, owner
}

func verifyKhoraDeadIdleRecovery(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, backend *khorabackend.Backend, processes *partialAbortBackend, previous execution.Binding, owner string) execution.Binding {
	t.Helper()
	controller.RecoveryReader = agentbridge.RecoveryReader{Backend: backend, Records: st}
	controller.RecoveryProjector = srv
	controller.NativeRestorer = driverstate.Restorer{Backend: backend}
	if err := backend.SignalProcess(ctx, sandbox.ProcessReference{StoreID: previous.StoreID, SandboxID: previous.SandboxID, Handle: previous.ProcessHandle, Epoch: previous.Epoch}, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DBForTest().Exec(`UPDATE runtime_generations SET lease_expires_at='2000-01-01T00:00:00Z' WHERE generation_id=?`, previous.GenerationID); err != nil {
		t.Fatal(err)
	}
	starts := processes.starts.Load()
	writer, ok := srv.workspaceWrites.(execution.WorkspaceRecovery)
	if !ok {
		t.Fatal("reconciliation writer unavailable")
	}
	stop := startKhoraReconciler(t, ctx, srv, st, controller, writer, owner)
	defer stop()
	for {
		var complete int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM execution_recoveries WHERE generation_id=? AND stage='complete' AND reason=?`, previous.GenerationID, execution.IdleAttachmentLostReason).Scan(&complete); err != nil {
			t.Fatal(err)
		}
		if complete == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	if processes.starts.Load() != starts {
		t.Fatal("failed idle attachment started another agent before journal recovery")
	}
	assertIntegrationCapacity(t, ctx, st, 0)
	replacement, err := controller.Ensure(ctx, execution.Session{ID: previous.SessionID, Driver: previous.Driver}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.GenerationID == previous.GenerationID || replacement.SandboxID != previous.SandboxID || replacement.Epoch != previous.Epoch+2 || processes.starts.Load() != starts+1 {
		t.Fatalf("dead idle recovery lost retained lifecycle: %+v -> %+v", previous, replacement)
	}
	return replacement
}

func waitCaptureMaintenance(t *testing.T, ctx context.Context, st *store.Store, session, want string) {
	t.Helper()
	for {
		var stage string
		if err := st.DBForTest().QueryRow(`SELECT stage FROM native_capture_activations WHERE session_id=?`, session).Scan(&stage); err != nil {
			t.Fatal(err)
		}
		if stage == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("capture maintenance remained %s waiting for %s: %v", stage, want, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func verifyKhoraPiControls(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, binding execution.Binding, owner string, entered <-chan struct{}) {
	t.Helper()
	turn, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "wait for steering and abort"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for index, message := range []string{"first steering instruction", "second steering instruction"} {
		queued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: message})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		session, err := st.GetSession(ctx, binding.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/turn/steer", nil).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
		response := httptest.NewRecorder()
		srv.steerQueuedTurn(response, request, session, snapshot, store.TurnSnapshot{ID: queued.TurnID, Content: message})
		want := http.StatusAccepted
		if index == 0 {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want {
			t.Fatalf("Pi process steer: %d %s", response.Code, response.Body.String())
		}
		if index == 0 {
			if _, err := st.ReleaseParkedTurn(ctx, store.ReleaseParkedTurnParams{SessionID: binding.SessionID, TurnID: queued.TurnID}); err == nil {
				t.Fatal("partial steering send allowed independent redispatch")
			}
			writer, ok := srv.workspaceWrites.(execution.WorkspaceRecovery)
			if !ok {
				t.Fatal("reconciliation writer unavailable")
			}
			stop := startKhoraReconciler(t, ctx, srv, st, controller, writer, owner)
			defer stop()
		}
		for {
			status, reason, err := st.TurnStatus(ctx, binding.SessionID, queued.TurnID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "completed" {
				break
			}
			if status != "queued" {
				t.Fatalf("steering outcome changed source: %s %s", status, reason)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		var count int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='user'`, queued.TurnID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("steering transcript not exactly once: %d %v", count, err)
		}
		if err := controller.DeliverTurnCommand(ctx, execution.TurnCommand{ID: fmt.Sprintf("control_steer_%s_%d", binding.GenerationID, queued.TurnID), SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, TurnID: turn.TurnID, Kind: "steer", Message: message}); err != nil {
			t.Fatalf("steer receipt retry: %v", err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/interrupt", nil).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
	response := httptest.NewRecorder()
	srv.interruptSession(response, request, binding.SessionID)
	if response.Code != http.StatusAccepted {
		t.Fatalf("Pi process abort: %d %s", response.Code, response.Body.String())
	}
	for {
		status, reason, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
		if err != nil {
			t.Fatal(err)
		}
		if status == "canceled" {
			break
		}
		if status == "completed" || status == "failed" {
			t.Fatalf("Pi abort outcome: %s %s", status, reason)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	var receipts int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM execution_turn_commands WHERE generation_id=? AND turn_id=? AND acknowledged=1`, binding.GenerationID, turn.TurnID).Scan(&receipts); err != nil || receipts != 3 {
		t.Fatalf("Pi control ledger: %d %v", receipts, err)
	}
}

func verifyKhoraInventory(t *testing.T, ctx context.Context, srv *Server, st *store.Store, session string, paused bool) {
	t.Helper()
	if err := st.UpsertArtifact(ctx, store.Artifact{SessionID: session, Path: "deleted-file", Size: 1, ModTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/artifacts", nil).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
	response := httptest.NewRecorder()
	srv.listArtifacts(response, request, session)
	if response.Code != http.StatusOK {
		t.Fatalf("inventory paused=%t: %d %s", paused, response.Code, response.Body.String())
	}
	var result struct {
		Artifacts []store.Artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, artifact := range result.Artifacts {
		paths[artifact.Path] = true
		if artifact.Path == "deleted-file" || strings.HasPrefix(artifact.Path, "symlink-parent/") || strings.Contains(artifact.Path, ".harness-upload-") {
			t.Fatalf("inventory exposed stale/symlink/staging path: %s", artifact.Path)
		}
	}
	if !paths["retained-controller-proof"] || !paths["nested/uploaded.txt"] {
		t.Fatalf("inventory missed guest/uploaded artifacts: %+v", paths)
	}
	if paused && !paths["paused-upload.txt"] {
		t.Fatal("paused inventory missed maintenance upload")
	}
	for i := 0; i < 40; i++ {
		if !paths[fmt.Sprintf("pages/file-%02d", i)] {
			t.Fatalf("directory pagination lost artifact %d", i)
		}
	}
	if paused {
		assertIntegrationCapacity(t, ctx, st, 0)
	}
}

type lostWorkspaceLifecycle struct {
	sandbox.Lifecycle
	resume, pause bool
}

func (b *lostWorkspaceLifecycle) Resume(ctx context.Context, r sandbox.Reference) (sandbox.Observation, error) {
	observed, err := b.Lifecycle.Resume(ctx, r)
	if err == nil && b.resume {
		b.resume = false
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("injected lost workspace resume reply")}
	}
	return observed, err
}
func (b *lostWorkspaceLifecycle) Pause(ctx context.Context, r sandbox.Reference) (sandbox.Observation, error) {
	observed, err := b.Lifecycle.Pause(ctx, r)
	if err == nil && b.pause {
		b.pause = false
		return sandbox.Observation{}, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("injected lost workspace pause reply")}
	}
	return observed, err
}
func verifyKhoraPausedWrite(t *testing.T, ctx context.Context, srv *Server, st *store.Store, backend interface {
	sandbox.Processes
	sandbox.Lifecycle
}, content *prepared.Store, session, owner string) {
	t.Helper()
	process := &lostReplacementBackend{Processes: backend, lose: true}
	lifecycle := &lostWorkspaceLifecycle{Lifecycle: backend, resume: true, pause: true}
	writer := sessionfiles.Writer{Repository: st, Backend: process, Lifecycle: lifecycle, Content: content, MaxActive: 2}
	srv.SetWorkspaceWriter(&writer)
	request := httptest.NewRequest(http.MethodPut, "/artifacts/"+session+"/paused-upload.txt", strings.NewReader("offline replacement")).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
	response := httptest.NewRecorder()
	srv.writeArtifact(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("lost offline resume HTTP outcome: %d %s", response.Code, response.Body.String())
	}
	mutations, err := st.ListUnresolvedWorkspaceMutations(ctx)
	if err != nil || len(mutations) != 1 {
		t.Fatalf("offline upload intent: %+v %v", mutations, err)
	}
	for _, fault := range []string{"helper reply", "pause reply"} {
		if _, err := writer.Recover(ctx, mutations[0].ID, owner); err == nil {
			t.Fatalf("lost %s ignored", fault)
		}
		if _, err := st.EnqueueTurn(ctx, session, "must wait for complete offline upload", time.Now()); err == nil {
			t.Fatal("offline upload released turn fence before final pause")
		}
	}
	if _, err := writer.Recover(ctx, mutations[0].ID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Recover(ctx, mutations[0].ID, owner); err != nil {
		t.Fatal(err)
	}
	if process.starts != 1 {
		t.Fatalf("offline upload retransmitted: %d", process.starts)
	}
	var active int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM execution_bindings WHERE session_id=? AND state!='retired'`, session).Scan(&active); err != nil || active != 0 {
		t.Fatalf("offline write launched agent: %d %v", active, err)
	}
	assertIntegrationCapacity(t, ctx, st, 0)
	reader, _, status, message := srv.openArtifactFile(ctx, session, "paused-upload.txt", accessOwner)
	if status != 0 {
		t.Fatalf("offline upload read: %d %s", status, message)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "offline replacement" {
		t.Fatalf("offline upload bytes: %q %v", data, err)
	}
	srv.SetWorkspaceWriter(&sessionfiles.Writer{Repository: st, Backend: backend, Lifecycle: backend, Content: content, MaxActive: 2})
}

type lostReplacementBackend struct {
	sandbox.Processes
	starts int
	lose   bool
}

func (b *lostReplacementBackend) StartProcess(ctx context.Context, p sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	stream, err := b.Processes.StartProcess(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, arg := range p.Arguments {
		if arg == "replace" {
			b.starts++
			if b.lose {
				b.lose = false
				return lostReplacementStream{stream}, nil
			}
		}
	}
	return stream, nil
}

type lostReplacementStream struct{ sandbox.ProcessStream }

func (s lostReplacementStream) Next() (sandbox.ProcessFrame, error) {
	frame, err := s.ProcessStream.Next()
	if err == nil && frame.Kind == "end" {
		return sandbox.ProcessFrame{}, errors.New("injected lost completed replacement reply")
	}
	return frame, err
}

func verifyKhoraWorkspaceWrite(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, backend interface {
	sandbox.Processes
	sandbox.Lifecycle
}, content *prepared.Store, session, owner string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/artifacts/"+session+"/nested/uploaded.txt", strings.NewReader("first upload")).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
	response := httptest.NewRecorder()
	srv.writeArtifact(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Khora HTTP write: %d %s", response.Code, response.Body.String())
	}
	fault := &lostReplacementBackend{Processes: backend, lose: true}
	writer := sessionfiles.Writer{Repository: st, Backend: fault, Content: content}
	srv.SetWorkspaceWriter(&writer)
	if _, err := srv.writeWorkspaceFile(ctx, session, "nested/uploaded.txt", strings.NewReader("recovered upload"), 1024); !errors.Is(err, workspace.ErrUncertain) {
		t.Fatalf("lost replacement reply hidden: %v", err)
	}
	mutations, err := st.ListUnresolvedWorkspaceMutations(ctx)
	if err != nil || len(mutations) != 1 {
		t.Fatalf("lost upload intent: %+v %v", mutations, err)
	}
	if _, err := st.EnqueueTurn(ctx, session, "must wait for upload", time.Now()); err == nil {
		t.Fatal("turn crossed unresolved replacement")
	}
	stop := startKhoraReconciler(t, ctx, srv, st, controller, &writer, owner)
	for {
		pending, err := st.ListUnresolvedWorkspaceMutations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	if _, err := writer.Recover(ctx, mutations[0].ID, owner); err != nil {
		t.Fatal(err)
	}
	if fault.starts != 1 {
		t.Fatal("recovery blindly redispatched replacement")
	}
	reader, _, status, message := srv.openArtifactFile(ctx, session, "nested/uploaded.txt", accessOwner)
	if status != 0 {
		t.Fatalf("recovered upload read: %d %s", status, message)
	}
	raw, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(raw) != "recovered upload" {
		t.Fatalf("recovered upload bytes: %q %v", raw, err)
	}
	fault.lose = true
	if _, err := writer.WriteSession(ctx, session, owner, "nested/uploaded.txt", strings.NewReader("second upload"), 1024); !errors.Is(err, workspace.ErrUncertain) {
		t.Fatalf("second lost reply: %v", err)
	}
	mutations, err = st.ListUnresolvedWorkspaceMutations(ctx)
	if err != nil || len(mutations) != 1 {
		t.Fatalf("second mutation intent: %+v %v", mutations, err)
	}
	environment, err := st.GetSessionEnvironment(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	files := guestfiles.Files{Backend: backend, Reference: sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch}, Root: "/workspace"}
	changed := "independent guest edit"
	if _, err := files.ReplacePrepared(ctx, "independent-guest-edit", mutations[0].Path, int64(len(changed)), fmt.Sprintf("%x", sha256.Sum256([]byte(changed))), strings.NewReader(changed)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Recover(ctx, mutations[0].ID, owner); err == nil {
		t.Fatal("mismatched destination was blindly accepted or overwritten")
	}
	remaining, err := st.ListUnresolvedWorkspaceMutations(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("verified failed replacement retained admission forever: %+v %v", remaining, err)
	}
	reader, _, status, message = srv.openArtifactFile(ctx, session, mutations[0].Path, accessOwner)
	if status != 0 {
		t.Fatalf("external edit read: %d %s", status, message)
	}
	raw, err = io.ReadAll(reader)
	reader.Close()
	if err != nil || string(raw) != changed || fault.starts != 2 {
		t.Fatalf("failed recovery overwrote later content: %q starts=%d %v", raw, fault.starts, err)
	}
	srv.SetWorkspaceWriter(&sessionfiles.Writer{Repository: st, Backend: backend, Lifecycle: backend, MaxActive: 2, Content: content})
}

func verifyKhoraWorkspaceDownload(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, session, owner string, paused bool) {
	t.Helper()
	reader, _, status, message := srv.openArtifactFile(ctx, session, "retained-controller-proof", accessOwner)
	if status != 0 || reader == nil {
		t.Fatalf("workspace open paused=%t: %d %s", paused, status, message)
	}
	if paused {
		if _, err := controller.Ensure(ctx, execution.Session{ID: session, Driver: "sh"}, owner); !errors.Is(err, execution.ErrBusy) {
			t.Fatalf("offline download did not fence resume: %v", err)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/artifacts/"+session+"/retained-controller-proof", nil).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
	request.Header.Set("Range", "bytes=8-13")
	response := httptest.NewRecorder()
	srv.getArtifact(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "worker" {
		t.Fatalf("workspace range paused=%t: %d %q", paused, response.Code, response.Body.String())
	}
	for _, path := range []string{"symlink-parent/retained-controller-proof", "missing"} {
		file, _, status, _ := srv.openArtifactFile(ctx, session, path, accessOwner)
		if file != nil {
			file.Close()
			t.Fatal("unexpected workspace file")
		}
		want := http.StatusForbidden
		if path == "missing" {
			want = http.StatusNotFound
		}
		if status != want {
			t.Fatalf("workspace refusal paused=%t path=%s status=%d want=%d", paused, path, status, want)
		}
	}
	var leases int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM workspace_read_leases WHERE session_id=?`, session).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("download leaked read lease: %d %v", leases, err)
	}
	if paused {
		assertIntegrationCapacity(t, ctx, st, 0)
	}
}

type captureFaultRepository struct {
	*store.Store
	fail bool
}

func verifyKhoraShellStop(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, binding execution.Binding, owner string) {
	t.Helper()
	turn, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "printf 'before-interrupt\\n'; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		var outputs int
		if err := st.DBForTest().QueryRow(`SELECT count(*) FROM bridge_ingress WHERE turn_id=? AND type='emit_output'`, turn.TurnID).Scan(&outputs); err != nil {
			t.Fatal(err)
		}
		if outputs > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	queued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: binding.SessionID, Content: "must remain parked"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+binding.SessionID+"/interrupt", nil).WithContext(context.WithValue(ctx, userContextKey{}, "lab"))
	response := httptest.NewRecorder()
	srv.interruptSession(response, request, binding.SessionID)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("shell process control: %d %s", response.Code, response.Body.String())
	}
	writer, ok := srv.workspaceWrites.(execution.WorkspaceRecovery)
	if !ok {
		t.Fatal("workspace reconciler not configured")
	}
	stop := startKhoraReconciler(t, ctx, srv, st, controller, writer, owner)
	defer stop()
	for {
		status, reason, err := st.TurnStatus(ctx, binding.SessionID, turn.TurnID)
		if err != nil {
			t.Fatal(err)
		}
		if status == "canceled" {
			break
		}
		if status == "completed" || status == "failed" {
			t.Fatalf("shell abort classification: %s %s", status, reason)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	snapshot, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
	if err != nil || len(snapshot.QueuedTurns) != 1 || !snapshot.QueuedTurns[0].Parked {
		t.Fatalf("Stop lost parked queue: %+v %v", snapshot, err)
	}
	command := execution.TurnCommand{ID: fmt.Sprintf("control_abort_%s_%d", binding.GenerationID, turn.TurnID), SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, TurnID: turn.TurnID, Kind: "abort"}
	if err := controller.DeliverTurnCommand(ctx, command); err != nil {
		t.Fatalf("durable abort retry: %v", err)
	}
	if _, err := st.CancelQueuedTurn(ctx, store.CancelQueuedTurnParams{SessionID: binding.SessionID, TurnID: queued.TurnID}); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetExecutionBinding(ctx, binding.GenerationID)
	if err != nil || current.State != "accepted" {
		t.Fatalf("Stop retired retained execution: %+v %v", current, err)
	}
}

type partialAbortBackend struct {
	sandbox.Processes
	starts, connects atomic.Int32
	fail             atomic.Bool
	failSteer        atomic.Bool
	failGrant        atomic.Bool
}

func (b *partialAbortBackend) StartProcess(ctx context.Context, p sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	b.starts.Add(1)
	return b.Processes.StartProcess(ctx, p)
}
func (b *partialAbortBackend) ConnectProcess(ctx context.Context, p sandbox.ProcessReference, after uint64) (sandbox.ProcessStream, error) {
	b.connects.Add(1)
	return b.Processes.ConnectProcess(ctx, p, after)
}

func (b *partialAbortBackend) SendProcessInput(ctx context.Context, reference sandbox.ProcessReference, data []byte) error {
	frames, err := (&bridge.StreamDecoder{}).Feed(data)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if frame.Kind == "command" && frame.Envelope != nil && frame.Envelope.Type == bridge.TypeGrant && b.failGrant.CompareAndSwap(true, false) {
			if err := b.Processes.SendProcessInput(ctx, reference, data[:len(data)/2]); err != nil {
				return err
			}
			return errors.New("injected partial grant reply")
		}
		if frame.Kind == "command" && ((strings.HasPrefix(frame.CommandID, "control_abort_") && b.fail.CompareAndSwap(true, false)) || (strings.HasPrefix(frame.CommandID, "control_steer_") && b.failSteer.CompareAndSwap(true, false))) {
			if err := b.Processes.SendProcessInput(ctx, reference, data[:len(data)/2]); err != nil {
				return err
			}
			return errors.New("injected partial abort input")
		}
	}
	return b.Processes.SendProcessInput(ctx, reference, data)
}

func startKhoraReconciler(t *testing.T, ctx context.Context, srv *Server, st *store.Store, controller *execution.Controller, writer execution.WorkspaceRecovery, owner string) func() {
	t.Helper()
	run, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	reconciler := &execution.Reconciler{Repository: st, Controller: controller, Workspace: writer, Owner: owner, MaxActive: 2, CaptureWindow: time.Minute, Interval: 20 * time.Millisecond, ActionTimeout: 5 * time.Second}
	var reported sync.Map
	reconciler.OnError = func(kind execution.ReconcileKind, key string, err error) {
		message := fmt.Sprintf("%s %s: %v", kind, key, err)
		if _, loaded := reported.LoadOrStore(message, true); !loaded {
			t.Logf("reconciliation retry: %s", message)
		}
	}
	go func() { finished <- reconciler.Run(run) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-finished:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("reconciler stopped: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Error("reconciler did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func (r *captureFaultRepository) CompleteNativeCapture(ctx context.Context, request nativecapture.Request, result nativecapture.Result) error {
	if r.fail {
		r.fail = false
		return errors.New("injected lost capture receipt before commit")
	}
	return r.Store.CompleteNativeCapture(ctx, request, result)
}

type nativeFaultRestorer struct {
	Native execution.NativeRestorer
	fail   bool
}

func (r *nativeFaultRestorer) RestoreCompletedNative(ctx context.Context, op execution.Operation, request execution.NativeRestoreRequest) error {
	if err := r.Native.RestoreCompletedNative(ctx, op, request); err != nil {
		return err
	}
	if r.fail {
		r.fail = false
		return errors.New("injected lost native restore reply")
	}
	return nil
}

func assertIntegrationCapacity(t *testing.T, ctx context.Context, st *store.Store, want int) {
	t.Helper()
	count, err := st.CountSandboxOccupyingSessions(ctx)
	if err != nil || count != want {
		t.Fatalf("environment capacity: %d want %d: %v", count, want, err)
	}
}

type recoveryFaultRecords struct {
	*store.Store
	fail bool
}

func (r *recoveryFaultRecords) CommitRecoveredJournalRecord(ctx context.Context, id string, fence int64, owner string, record execution.RecoveredJournalRecord) (int64, error) {
	event, err := r.Store.CommitRecoveredJournalRecord(ctx, id, fence, owner, record)
	if err == nil && r.fail && record.Type == "emit_output" {
		r.fail = false
		return 0, errors.New("injected lost recovery ingest reply")
	}
	return event, err
}

type recoveryFaultReader struct {
	Reader execution.JournalRecoveryReader
	fail   bool
}

func (r *recoveryFaultReader) Recover(ctx context.Context, op execution.Operation, origin execution.Binding, owner string, request execution.JournalRecoveryRequest) (uint64, error) {
	head, err := r.Reader.Recover(ctx, op, origin, owner, request)
	if err == nil && r.fail {
		r.fail = false
		return 0, errors.New("injected lost stopped-reader reply")
	}
	return head, err
}

func runHistoricalRecoveryProbe(t *testing.T, ctx context.Context, backend sandbox.Processes, binding execution.Binding, handle, script string) {
	t.Helper()
	stream, err := backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/usr/bin/python3", Arguments: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var diagnostics strings.Builder
	for {
		frame, err := stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		if frame.Gap {
			t.Fatal("journal observation process gap")
		}
		if frame.Kind == "data" {
			diagnostics.Write(frame.Data)
		}
		if frame.Kind == "end" {
			if frame.ExitCode != 0 {
				t.Fatalf("journal observation failed: %s", diagnostics.String())
			}
			return
		}
	}
}

type acceptanceFaultRepository struct {
	*store.Store
	fail bool
}

func (r *acceptanceFaultRepository) AcceptExecutionBinding(ctx context.Context, p execution.ObserveBindingParams) error {
	if r.fail {
		r.fail = false
		return errors.New("injected failure after protocol readiness before acceptance")
	}
	return r.Store.AcceptExecutionBinding(ctx, p)
}
