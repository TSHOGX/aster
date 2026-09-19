//go:build khoraintegration

package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
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
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/nativecapture"
	capturesource "harness-platform/orchestrator/internal/nativecapture/guest"
	"harness-platform/orchestrator/internal/sandbox"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/trajectory"
	"harness-platform/orchestrator/internal/workspace"
	guestfiles "harness-platform/orchestrator/internal/workspace/guest"
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
	session := store.Session{ID: "khora-worker-" + driver + "-session", UserID: "lab", Agent: driver, Mode: store.ModeForDriver(driver), Status: "created", Workspace: "opaque-workspace", RestoreID: "unused", CreatedAt: now, UpdatedAt: now}
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
	srv.nativeStateValidator = func(ctx context.Context, input store.DriverStateValidationInput, turn int64) (workspace.File, error) {
		return driverstate.ValidateRemote(ctx, input.CanonicalPayload, fmt.Sprint(turn), "native-state-"+driver+"-"+fmt.Sprint(turn), guestfiles.Files{Backend: backend, Reference: input.Reference, Root: "/root/.pi/agent/sessions"})
	}
	var interruptedGeneration atomic.Pointer[string]
	interrupted, detached := make(chan struct{}, 1), make(chan struct{}, 1)
	launcher := agentbridge.Launcher{Backend: backend, Store: store.BoundBridgeStore{Store: st}, Receipts: st, LeaseTTL: time.Minute, BeforeComplete: srv.projectBeforeBridgeCompletion, AfterEventCommit: srv.publishDurableEvent}
	launcher.AfterCommit = func(ctx context.Context, envelope bridge.Envelope, event int64) {
		srv.handleBridgeCommittedEnvelope(ctx, envelope, event)
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
	plan := execution.LaunchPlan{TemplateID: template, SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20, Mounts: mounts, Network: network, Environment: processEnvironment}
	repository := &acceptanceFaultRepository{Store: st, fail: driver == "sh"}
	controller := &execution.Controller{Repository: repository, Backend: backend, Agents: launcher, Plans: integrationLaunchPlan{plan}, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
	defer func() { controller.Close() }()
	if driver == "sh" {
		if _, err := controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner); err == nil {
			t.Fatal("injected acceptance failure ignored")
		}
	}
	binding, err := controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner)
	if err != nil {
		t.Fatal(err)
	}
	initial := binding
	var initialNativeID, initialNativeFile string
	for cycle := 0; cycle < 3; cycle++ {
		if cycle == 1 {
			if err := controller.ReleaseIdle(ctx, session.ID, binding.GenerationID, leaseOwner); err != nil {
				t.Fatal(err)
			}
			assertIntegrationCapacity(t, ctx, st, 0)
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
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			replacement, err := store.AcquireOwnerLock(filepath.Join(t.TempDir(), "replacement-owner"))
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Close()
			if err := st.WriteOwner(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			leaseOwner = store.GenerationLeaseOwner(replacement.UUID)
			controller = &execution.Controller{Repository: st, Backend: backend, Agents: launcher, Plans: integrationLaunchPlan{plan}, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
			binding, err = controller.RecoverIdle(ctx, execution.Session{ID: session.ID, Driver: driver}, binding.GenerationID, leaseOwner)
			if err != nil {
				t.Fatal(err)
			}
			if binding.SandboxID != initial.SandboxID || binding.Epoch != initial.Epoch+2 || binding.Owner != leaseOwner {
				t.Fatalf("idle owner recovery lost retained environment: %+v", binding)
			}
		}
		content := "printf 'product-worker-ok\\n'"
		if driver == "sh" {
			if cycle == 0 {
				content = "printf 'product-worker-ok\\n' > /workspace/retained-controller-proof; cat /workspace/retained-controller-proof"
			} else {
				content = "cat /workspace/retained-controller-proof"
			}
		}
		enqueued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: session.ID, Content: content})
		if err != nil {
			t.Fatal(err)
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
	}
	{
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
		runHistoricalRecoveryProbe(t, ctx, backend, binding, probe)
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
			if err := st.DBForTest().QueryRow(`SELECT c.native_digest,c.selected_path,d.state_version FROM native_capture_requests c JOIN session_driver_states d ON d.session_id=c.session_id AND d.state_version=c.state_version WHERE c.session_id=?`, session.ID).Scan(&digest, &path, &version); err != nil || version != 4 {
				t.Fatalf("failed native turn advanced state: %d %v", version, err)
			}
			controller = &execution.Controller{Repository: st, Backend: backend, Agents: launcher, Plans: integrationLaunchPlan{plan}, BackendName: "engine", LeaseTTL: time.Minute, Lifetime: ctx, Routes: gateway.NewFlows()}
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
			if err := st.DBForTest().QueryRow(`SELECT state_version FROM session_driver_states WHERE session_id=?`, session.ID).Scan(&advanced); err != nil || advanced != 5 {
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
			if _, err := controller.Ensure(ctx, execution.Session{ID: session.ID, Driver: driver}, leaseOwner); err != nil {
				t.Fatal(err)
			}
			if n, err := capture.Step(ctx, 32); err == nil || n != 0 {
				t.Fatalf("lost capture receipt not surfaced: %d %v", n, err)
			}
			for range 4 {
				if n, err := capture.Step(ctx, 32); err != nil || n != 1 {
					t.Fatalf("completed snapshot capture: %d %v", n, err)
				}
			}
			if n, err := capture.Step(ctx, 32); err != nil || n != 0 {
				t.Fatalf("capture replay duplicated work: %d %v", n, err)
			}
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
			if err := mem.DBForTest().QueryRow(`SELECT count(*) FROM raw_sources WHERE session_id=?`, session.ID).Scan(&archives); err != nil || archives != 4 {
				t.Fatalf("capture retry changed consumer archive count: %d %v", archives, err)
			}
		}
		t.Logf("historical recovery origin=%s epoch=%d head=%d preserved failed turn=%d", binding.GenerationID, binding.Epoch, head, turn.TurnID)
	}

}

type captureFaultRepository struct {
	*store.Store
	fail bool
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

func runHistoricalRecoveryProbe(t *testing.T, ctx context.Context, backend sandbox.Processes, binding execution.Binding, script string) {
	t.Helper()
	stream, err := backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch, Handle: "inspect-retained-journal"}, IdempotencyKey: "inspect-retained-journal", Command: "/usr/bin/python3", Arguments: []string{"-c", script}})
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

type integrationLaunchPlan struct{ plan execution.LaunchPlan }

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

func (p integrationLaunchPlan) Plan(context.Context, execution.Session, string) (execution.LaunchPlan, error) {
	return p.plan, nil
}
