//go:build khoraintegration

package retainedapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/store"

	sdk "github.com/TSHOGX/khora/pkg/khora"
)

// TestKhoraComposedModelGatewayCarriesRealTraffic is the end-to-end vertical
// slice: a daemon-created Pi turn reaches a deterministic upstream only through
// the composed model gateway, over real Khora steering.
//
// The upstream listens on an address the guest is never allowed to reach
// directly, so a generation that produced a completion necessarily went through
// the gateway. The gateway holds the only credential and the application
// authorizes every request.
func TestKhoraComposedModelGatewayCarriesRealTraffic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	endpoint, template, manifestPath := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE"), os.Getenv("KHORA_INTEGRATION_MANIFEST")
	if endpoint == "" || template == "" || manifestPath == "" {
		t.Fatal("isolated engine/template/exported manifest required")
	}
	backend, err := khorabackend.New(ctx, khorabackend.Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := backend.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	// The upstream model. Its address is deliberately outside anything the
	// compiled policy allows the guest to reach directly.
	var upstreamRequests atomic.Int64
	var credentials sync.Map
	upstreamListener, err := net.Listen("tcp4", "198.18.0.1:0")
	if err != nil {
		// The substrate harness publishes 198.18.0.0/30; fall back to the
		// contract interface when it is absent so the failure names the cause.
		upstreamListener, err = net.Listen("tcp4", "192.0.2.1:0")
		if err != nil {
			t.Fatalf("isolated upstream address required: %v", err)
		}
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		credentials.Store(r.Header.Get("X-Api-Key"), true)
		if r.Header.Get("Authorization") != "" {
			t.Error("guest authorization header reached the upstream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`{"type":"message_start","message":{"id":"gateway-message","type":"message","role":"assistant","model":"approved-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"gateway-carried-ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		} {
			var event struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(frame), &event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, frame)
			w.(http.Flusher).Flush()
		}
	})}
	upstreamDone := make(chan error, 1)
	go func() { upstreamDone <- upstream.Serve(upstreamListener) }()
	defer func() { _ = upstream.Close(); <-upstreamDone }()

	// The gateway listens where the compiled route steers guest traffic.
	gatewayListener, err := net.Listen("tcp4", "192.0.2.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayEndpoint := netip.MustParseAddrPort(gatewayListener.Addr().String())
	reserved := netip.MustParsePrefix("10.80.0.0/24")

	catalog := egress.Catalog{
		Revision:  "integration-v1",
		Endpoints: []egress.Endpoint{{ID: "model", NextHop: "model", Transport: "tcp", Hostname: "model.internal", Port: 443}},
		Policies:  map[string]egress.Policy{"pi": {Revision: "integration-v1", Driver: "pi", Endpoints: []egress.EndpointAccess{{EndpointID: "model"}}}},
		Deployment: egress.Deployment{
			BackendID: identity.StoreID, SyntheticRange: reserved,
			Gateways: []egress.Gateway{{Endpoint: gatewayEndpoint, ID: "model"}},
		},
	}
	if err := egress.ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	origin, model := "http://model.internal:443", "approved-model"

	// Authored Pi configuration points at the guest-visible service origin, which
	// resolves through the launcher-projected hosts entry to the reserved address.
	configRoot := filepath.Join(root, "authored-config")
	if err := os.MkdirAll(filepath.Join(configRoot, "pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"harness_anthropic_proxy": map[string]any{"baseUrl": origin, "api": "anthropic-messages", "apiKey": "harness-placeholder", "models": []map[string]any{{"id": model, "name": model, "reasoning": false, "input": []string{"text"}, "contextWindow": 8192, "maxTokens": 256, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}}}
	encoded, _ := json.Marshal(models)
	for name, body := range map[string][]byte{"pi/models.json": encoded, "pi/settings.json": []byte(`{"compaction":{"enabled":false}}`)} {
		if err := os.WriteFile(filepath.Join(configRoot, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	revision, err := content.Read(configRoot)
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
	plans, err := launchplan.Load(strings.NewReader(string(raw)), digest, map[string]execution.LaunchPlan{"pi": {
		TemplateID: template, CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20,
		Content:     []execution.ContentRequirement{{Purpose: "config", Destination: "/harness-config", RevisionDigest: revision.Digest}},
		Environment: []string{"ANTHROPIC_BASE_URL=" + origin, "PI_MODEL=" + model},
	}})
	if err != nil {
		t.Fatal(err)
	}

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

	// The gateway runs as its own process under the engine's second-hop UID. The
	// engine refuses an egress.via declaration without one, because the endpoint
	// process's own egress can only be pinned by uid, so this is a real deployment
	// requirement rather than test scaffolding — and running the shipped command
	// proves the deployable binary, not a library shortcut.
	viaUID, viaGID := lookupViaIdentity(t)
	// The gateway UID must traverse to its own binary, credential and sockets, and
	// Unix sockets need a filesystem that supports them — the harness's disposable
	// btrfs TMPDIR provides neither — so everything the gateway reads lives in a
	// dedicated runtime directory.
	gatewayRoot, err := os.MkdirTemp("/run", "harness-gateway-")
	if err != nil {
		t.Fatalf("socket-capable runtime directory required: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(gatewayRoot) })
	if err := os.Chmod(gatewayRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(gatewayRoot, "upstream.key")
	if err := os.WriteFile(credentialPath, []byte("gateway-only-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(credentialPath, viaUID, viaGID); err != nil {
		t.Fatalf("credential must be readable only by the gateway UID: %v", err)
	}
	// The gateway creates its own control socket, so its directory must be writable
	// by that UID. The application's authorization socket stays in the parent,
	// which the gateway only connects to.
	gatewaySockets := filepath.Join(gatewayRoot, "control")
	if err := os.Mkdir(gatewaySockets, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(gatewaySockets, viaUID, viaGID); err != nil {
		t.Fatal(err)
	}
	authorizationSocket := filepath.Join(gatewayRoot, "authorization.sock")
	gatewayControl := filepath.Join(gatewaySockets, "gateway-control.sock")
	gatewayConfig := filepath.Join(gatewayRoot, "gateway.json")
	gatewayFile := map[string]any{
		"version": 1, "id": "model", "backend_id": identity.StoreID,
		"listen": gatewayEndpoint.String(), "reserved": reserved.String(),
		"control": gatewayControl, "application_uid": os.Getuid(), "control_group": viaGID,
		"authorization": authorizationSocket, "request_timeout": "30s",
		"upstreams": []any{map[string]any{"reference": "models", "base_url": "http://" + upstreamListener.Addr().String(), "api_key_header": "X-Api-Key", "api_key_file": credentialPath}},
	}
	encodedGateway, _ := json.Marshal(gatewayFile)
	if err := os.WriteFile(gatewayConfig, encodedGateway, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = gatewayListener.Close()

	authorizationListener, err := gateway.ListenAuthorization(authorizationSocket, viaGID)
	if err != nil {
		t.Fatal(err)
	}
	authorizationServer := &http.Server{Handler: gateway.AuthorizationHandler(st, uint32(viaUID)), ConnContext: gateway.PeerContext}
	authorizationDone := make(chan error, 1)
	go func() { authorizationDone <- authorizationServer.Serve(authorizationListener) }()
	defer func() { _ = authorizationServer.Close(); <-authorizationDone }()

	gatewayBinary := filepath.Join(gatewayRoot, "model-gateway")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", gatewayBinary, "../../cmd/model-gateway").CombinedOutput(); err != nil {
		t.Fatalf("build model gateway: %v %s", err, output)
	}
	if err := os.Chmod(gatewayBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	gatewayLog := filepath.Join(gatewayRoot, "gateway.log")
	logFile, err := os.Create(gatewayLog)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	gatewayProcess := exec.CommandContext(ctx, gatewayBinary, "--config", gatewayConfig)
	gatewayProcess.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(viaUID), Gid: uint32(viaGID)}}
	gatewayProcess.Stdout, gatewayProcess.Stderr = logFile, logFile
	if err := gatewayProcess.Start(); err != nil {
		t.Fatal(err)
	}
	gatewayExited := make(chan error, 1)
	go func() { gatewayExited <- gatewayProcess.Wait() }()
	defer func() {
		_ = gatewayProcess.Process.Signal(syscall.SIGTERM)
		select {
		case <-gatewayExited:
		case <-time.After(20 * time.Second):
			_ = gatewayProcess.Process.Kill()
			<-gatewayExited
		}
		if t.Failed() {
			if output, err := os.ReadFile(gatewayLog); err == nil {
				t.Log(string(output))
			}
		}
	}()
	// The application controls this gateway over its Unix socket, which also
	// confirms the process is up before any session is admitted.
	var remotes *gateway.Remotes
	for attempt := 0; ; attempt++ {
		handshake, cancelHandshake := context.WithTimeout(ctx, 2*time.Second)
		remotes, err = gateway.NewRemotes(handshake, []gateway.RemoteConfig{{ID: "model", Socket: gatewayControl, PeerUID: uint32(viaUID), Timeout: 10 * time.Second}})
		cancelHandshake()
		if err == nil {
			break
		}
		if attempt > 100 {
			t.Fatalf("gateway control never answered: %v", err)
		}
		select {
		case exitErr := <-gatewayExited:
			output, _ := os.ReadFile(gatewayLog)
			t.Fatalf("gateway exited before serving: %v\n%s", exitErr, output)
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer remotes.Close()

	app := server.New(config.Config{MaxSessions: 2, MaxEditBytes: 1 << 20, MaxUploadBytes: 1 << 20, MaxUploadFiles: 8}, st, events.NewHub(), testLogger())
	runtime, err := Attach(ctx, app, st, Options{
		Backend: backend, Plans: plans, Connectivity: catalog,
		Content: []AuthoredContent{{Purpose: "config", Root: configRoot, Revision: revision}}, ContentBytes: 8 << 20,
		Routes: remotes, Activator: remotes, Grants: remotes,
		BackendName: "gateway-engine", OwnerUUID: owner.UUID, PreparedRoot: filepath.Join(root, "prepared"),
		MaxActive: 2, InventoryLimit: 10000, LeaseTTL: time.Minute,
		PollInterval: 50 * time.Millisecond, EventInterval: 50 * time.Millisecond, ActionTimeout: 10 * time.Second,
		CaptureWindow: time.Minute, InventoryInterval: 200 * time.Millisecond,
		OnError: func(scope string, err error) { t.Logf("%s: %v", scope, err) },
	})
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
		case <-time.After(15 * time.Second):
			t.Error("composed services did not stop")
		}
	}()

	client, _ := sdk.New(endpoint)
	session := createIntegrationSession(t, ctx, app, "agent")
	defer func() {
		runtime.Close()
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if environment, err := st.GetSessionEnvironment(cleanup, session); err == nil {
			_ = client.Destroy(cleanup, environment.SandboxID)
		}
		for _, purpose := range []string{"workspace", "bridge"} {
			if binding, found, err := st.GetStorageBinding(cleanup, session, purpose); err == nil && found {
				_ = client.DeleteVolume(cleanup, binding.VolumeID)
			}
		}
		if rows, err := st.ListContentPublications(cleanup, identity.StoreID); err == nil {
			for _, row := range rows {
				for _, volume := range []string{row.PublishedVolumeID, row.StagingVolumeID} {
					if volume != "" {
						_ = client.DeleteVolume(cleanup, volume)
					}
				}
			}
		}
	}()

	turn := sendIntegrationTurn(t, ctx, app, session, "say hello through the gateway")
	awaitIntegrationTurn(t, ctx, st, session, turn)

	if upstreamRequests.Load() == 0 {
		t.Fatal("no model request reached the upstream through the composed gateway")
	}
	if _, leaked := credentials.Load("harness-placeholder"); leaked {
		t.Fatal("the guest placeholder reached the upstream as a credential")
	}
	if _, present := credentials.Load("gateway-only-secret"); !present {
		t.Fatal("the upstream did not receive the gateway's own credential")
	}
	var completions int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='assistant' AND content LIKE '%gateway-carried-ok%'`, turn).Scan(&completions); err != nil || completions == 0 {
		t.Fatalf("gateway-carried completion was not committed: %d %v", completions, err)
	}
	// Authority was recorded per request, attributed to this turn.
	var authorized int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_requests WHERE turn_id=? AND endpoint_id='model' AND operation='generate' AND outcome IS NOT NULL`, turn).Scan(&authorized); err != nil || authorized == 0 {
		t.Fatalf("model requests were not authorized and settled: %d %v", authorized, err)
	}
	var active int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_route_bindings WHERE state='active' AND endpoint_id='model'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("composed route was not activated: %d %v", active, err)
	}

	// Releasing the execution revokes that authority: a later request on the same
	// destination must be denied even though the route once worked.
	binding, found, err := st.FindActiveExecutionBinding(ctx, session)
	if err != nil || !found {
		t.Fatalf("active binding: %v %v", found, err)
	}
	if err := runtime.Controller.ReleaseIdle(ctx, session, binding.GenerationID, store.GenerationLeaseOwner(owner.UUID)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, egress.AuthorizationRequest{
		BackendID: identity.StoreID, Destination: netip.MustParseAddrPort("10.80.0.1:443"),
		RequestID: "after-release",
	}); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("released execution retained service authority: %v", err)
	}
	var revoked int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_route_bindings WHERE state='revoked'`).Scan(&revoked); err != nil || revoked == 0 {
		t.Fatalf("idle release did not revoke composed routes: %d %v", revoked, err)
	}
}

func testLogger() *slog.Logger { return slog.Default() }

func createIntegrationSession(t *testing.T, ctx context.Context, app *server.Server, mode string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"mode":"`+mode+`"}`)).WithContext(ctx)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("session creation: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("created session identity: %v", err)
	}
	return created.ID
}

func sendIntegrationTurn(t *testing.T, ctx context.Context, app *server.Server, session, prompt string) int64 {
	t.Helper()
	body := `{"content":` + string(mustJSON(prompt)) + `}`
	for {
		request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session+"/messages", strings.NewReader(body)).WithContext(ctx)
		response := httptest.NewRecorder()
		app.Routes().ServeHTTP(response, request)
		if response.Code == http.StatusAccepted {
			var accepted struct {
				ID int64 `json:"turn_id"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
				t.Fatal(err)
			}
			return accepted.ID
		}
		if response.Code != http.StatusConflict {
			t.Fatalf("turn submission: %d %s", response.Code, response.Body.String())
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func awaitIntegrationTurn(t *testing.T, ctx context.Context, st *store.Store, session string, turn int64) {
	t.Helper()
	for {
		status, reason, err := st.TurnStatus(ctx, session, turn)
		if err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			return
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("turn %d: %s %s", turn, status, reason)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("turn %d did not complete: %v", turn, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// lookupViaIdentity resolves the engine's configured second-hop UID. The engine
// refuses an egress.via declaration without one, so a deployment must run its
// protocol gateways under that identity.
func lookupViaIdentity(t *testing.T) (int, int) {
	t.Helper()
	account, err := user.Lookup("khoravia")
	if err != nil {
		t.Fatalf("engine second-hop account required: %v", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		t.Fatal(err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		t.Fatal(err)
	}
	return uid, gid
}
