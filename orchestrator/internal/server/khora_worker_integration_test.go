//go:build khoraintegration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/agentbridge"
	"harness-platform/orchestrator/internal/driverstate"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	guestfiles "harness-platform/orchestrator/internal/workspace/guest"
)

func TestKhoraExecutionWorkerCommitsTurnMessagesAndJournal(t *testing.T) { runKhoraWorker(t, "sh") }
func TestKhoraPiExecutionWorkerCommitsNativeState(t *testing.T)          { runKhoraWorker(t, "pi") }
func runKhoraWorker(t *testing.T, driver string) {
	endpoint, template := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE")
	if endpoint == "" || template == "" {
		t.Fatal("isolated Khora endpoint/template required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	now := time.Now().UTC()
	session := store.Session{ID: "khora-worker-" + driver + "-session", UserID: "lab", Agent: driver, Mode: store.ModeForDriver(driver), Status: "created", Workspace: "opaque-workspace", RestoreID: "unused", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: "engine", StoreID: identity.StoreID, Version: identity.Version}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEnvironmentBinding(ctx, execution.Environment{ID: "worker-" + driver + "-environment", SessionID: session.ID, BackendName: "engine", StoreID: identity.StoreID, Desired: json.RawMessage(`{"template":"battery"}`)}); err != nil {
		t.Fatal(err)
	}
	storage := execution.StorageService{Repository: st, Backend: backend}
	var mounts []sandbox.Mount
	var volumes []string
	for _, purpose := range []string{"workspace", "bridge"} {
		binding, err := storage.EnsureVolume(ctx, "worker-"+driver+"-environment", purpose, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		volumes = append(volumes, binding.VolumeID)
		destination := "/workspace"
		if purpose == "bridge" {
			destination = "/harness-bridge"
		}
		mounts = append(mounts, sandbox.Mount{VolumeID: binding.VolumeID, Destination: destination, Access: "exclusive"})
	}
	environment, err := st.GetEnvironmentBinding(ctx, "worker-"+driver+"-environment")
	if err != nil {
		t.Fatal(err)
	}
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
	request := sandbox.CreateRequest{IdempotencyKey: "worker-" + driver + "-create-key", TemplateID: template, CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, Mounts: mounts, Network: network}
	raw, _ := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: identity.StoreID}, Create: &request})
	observed, err := (execution.EnvironmentService{Repository: st, Backend: backend}).Run(ctx, execution.OperationIntent{ID: "worker-" + driver + "-create", EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: request.IdempotencyKey, ExpectedFence: environment.Fence, Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = client.Destroy(cleanup, observed.SandboxID)
		for _, volume := range volumes {
			_ = client.DeleteVolume(cleanup, volume)
		}
	})
	srv := &Server{store: st, hub: events.NewHub(), log: slog.Default(), cfg: testServerConfig(directory)}
	srv.nativeStateValidator = func(ctx context.Context, input store.DriverStateValidationInput, turn int64) (workspace.File, error) {
		return driverstate.ValidateRemote(ctx, input.CanonicalPayload, fmt.Sprint(turn), "native-state-"+driver+"-"+fmt.Sprint(turn), guestfiles.Files{Backend: backend, Reference: input.Reference, Root: "/root/.pi/agent/sessions"})
	}
	launcher := agentbridge.Launcher{Backend: backend, Store: store.BoundBridgeStore{Store: st}, Receipts: st, LeaseTTL: time.Minute, AfterCommit: srv.handleBridgeCommittedEnvelope, BeforeComplete: srv.projectBeforeBridgeCompletion, AfterEventCommit: srv.publishDurableEvent}
	worker := execution.Worker{Repository: st, Agents: launcher}
	binding, connection, err := worker.Start(ctx, execution.WorkerRequest{Reserve: execution.ReserveBindingParams{GenerationID: "khora-worker-" + driver + "-generation", SessionID: session.ID, EnvironmentID: environment.ID, Owner: store.GenerationLeaseOwner(owner.UUID), Driver: driver, SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64), LeaseTTL: time.Minute}, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: identity.StoreID, SandboxID: observed.SandboxID, Epoch: observed.Epoch, Handle: "product-worker-" + driver + "-bridge"}, IdempotencyKey: "worker-" + driver + "-start", Command: "/usr/local/bin/harness-khora-launcher", Arguments: []string{"--session-id", session.ID, "--generation-id", "khora-worker-" + driver + "-generation", "--driver-id", driver}, Directory: "/workspace", Environment: processEnvironment}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	enqueued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: session.ID, Content: "printf 'product-worker-ok\\n'"})
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
		if message.Role == "assistant" && strings.Contains(message.Content, "product-worker-ok") {
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
		if version != 2 || !strings.Contains(payload, "/root/.pi/agent/sessions") || !strings.Contains(payload, "selected_session_id") {
			t.Fatalf("native Pi state not committed: version=%d payload=%s", version, payload)
		}
	}
	t.Logf("accepted execution=%s epoch=%d turn=%d messages=%d journal_watermark=%d", binding.GenerationID, binding.Epoch, enqueued.TurnID, len(messages), watermark)
}
