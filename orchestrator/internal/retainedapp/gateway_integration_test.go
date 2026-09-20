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
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	"harness-platform/orchestrator/internal/sandbox"
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
func TestKhoraComposedModelGatewayCarriesRealTraffic(t *testing.T) { runComposedGateway(t, true) }
func TestKhoraNetworkBoundaryUsesDeclaredTCPAndUDP(t *testing.T)   { runComposedGateway(t, false) }
func runComposedGateway(t *testing.T, fullFaults bool) {
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
	var rejectAuthentication atomic.Bool
	var holdModel atomic.Bool
	modelStarted, modelClosed := make(chan struct{}, 1), make(chan struct{}, 1)
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
		if rejectAuthentication.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"controlled rejection"}}`))
			return
		}
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
			if event.Type == "message_start" && holdModel.Load() {
				modelStarted <- struct{}{}
				<-r.Context().Done()
				modelClosed <- struct{}{}
				return
			}
		}
	})}
	upstreamDone := make(chan error, 1)
	go func() { upstreamDone <- upstream.Serve(upstreamListener) }()
	defer func() { _ = upstream.Close(); <-upstreamDone }()

	allowedTCP, err := net.Listen("tcp4", "198.18.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		for {
			connection, err := allowedTCP.Accept()
			if err != nil {
				return
			}
			_, _ = connection.Write([]byte("direct-ok"))
			_ = connection.Close()
		}
	}()
	defer func() { _ = allowedTCP.Close(); <-tcpDone }()
	allowedUDP, err := net.ListenPacket("udp4", "198.18.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpDone := make(chan struct{})
	go func() {
		defer close(udpDone)
		data := make([]byte, 512)
		for {
			n, peer, err := allowedUDP.ReadFrom(data)
			if err != nil {
				return
			}
			_, _ = allowedUDP.WriteTo(data[:n], peer)
		}
	}()
	defer func() { _ = allowedUDP.Close(); <-udpDone }()
	tcpAddress := netip.MustParseAddrPort(allowedTCP.Addr().String())
	udpAddress := netip.MustParseAddrPort(allowedUDP.LocalAddr().String())

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
		Policies:  map[string]egress.Policy{"pi": {Revision: "integration-v1", Driver: "pi", Endpoints: []egress.EndpointAccess{{EndpointID: "model"}}, Direct: []egress.Tuple{{CIDR: netip.PrefixFrom(tcpAddress.Addr(), 32), Port: tcpAddress.Port(), Protocol: "tcp"}, {CIDR: netip.PrefixFrom(udpAddress.Addr(), 32), Port: udpAddress.Port(), Protocol: "udp"}}}},
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
	for name, body := range map[string][]byte{"pi/models.json": encoded, "pi/settings.json": []byte(`{"defaultProvider":"harness_anthropic_proxy","defaultModel":"approved-model","compaction":{"enabled":false}}`)} {
		if err := os.WriteFile(filepath.Join(configRoot, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := launchplan.Digest(raw)
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
		"authorization": authorizationSocket, "request_timeout": "90s",
		"upstreams": []any{map[string]any{"endpoint_id": "model", "models": []string{"approved-model"}, "base_url": "http://" + upstreamListener.Addr().String(), "api_key_header": "X-Api-Key", "api_key_file": credentialPath}},
	}
	encodedGateway, _ := json.Marshal(gatewayFile)
	if err := os.WriteFile(gatewayConfig, encodedGateway, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = gatewayListener.Close()

	startAuthorization := func() (*http.Server, chan error) {
		listener, err := gateway.ListenAuthorization(authorizationSocket, viaGID)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: gateway.AuthorizationHandler(routeDiagnostics{Store: st, t: t}, uint32(viaUID)), ConnContext: gateway.PeerContext}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		return server, done
	}
	authorizationServer, authorizationDone := startAuthorization()
	authorizationRunning := true
	defer func() {
		if authorizationRunning {
			_ = authorizationServer.Close()
			<-authorizationDone
		}
	}()

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
	startGateway := func() (*exec.Cmd, chan error) {
		process := exec.CommandContext(ctx, gatewayBinary, "--config", gatewayConfig)
		process.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(viaUID), Gid: uint32(viaGID)}}
		process.Stdout, process.Stderr = logFile, logFile
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- process.Wait() }()
		return process, exited
	}
	gatewayProcess, gatewayExited := startGateway()
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
	remoteConfig := gateway.RemoteConfig{ID: "model", Socket: gatewayControl, PeerUID: uint32(viaUID), Timeout: 10 * time.Second}
	for attempt := 0; ; attempt++ {
		handshake, cancelHandshake := context.WithTimeout(ctx, 2*time.Second)
		peer, err := gateway.NewRemote(handshake, remoteConfig)
		cancelHandshake()
		if err == nil {
			peer.Close()
			break
		}
		if attempt > 100 {
			t.Fatalf("gateway did not start: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	remotes, err := gateway.NewRemotes(ctx, []gateway.RemoteConfig{remoteConfig}, st)
	if err != nil {
		t.Fatal(err)
	}
	defer remotes.Close()

	app := server.New(config.Config{MaxSessions: 2, MaxEditBytes: 1 << 20, MaxUploadBytes: 1 << 20, MaxUploadFiles: 8}, st, events.NewHub(), testLogger())

	deploymentPath := filepath.Join(root, "deployment.json")
	document := map[string]any{
		"version": 1, "backend_name": "gateway-engine", "engine": map[string]any{"endpoint": endpoint, "store_id": identity.StoreID},
		"manifest": manifestPath, "manifest_digest": digest,
		"plans":         map[string]any{"pi": map[string]any{"template": template, "cpu_millis": 1000, "memory_bytes": 1 << 30, "disk_bytes": 256 << 20, "workspace_bytes": 64 << 20, "journal_bytes": 64 << 20, "config": configRoot}},
		"content_bytes": 8 << 20, "prepared_root": filepath.Join(root, "prepared"),
		"authorization": map[string]any{"socket": authorizationSocket, "peer_uid": viaUID, "group": viaGID},
		"gateways":      []any{map[string]any{"id": "model", "socket": gatewayControl, "peer_uid": viaUID, "listener": gatewayEndpoint.String()}},
		"connectivity":  map[string]any{"revision": catalog.Revision, "synthetic_range": reserved.String(), "catalog": catalog.Endpoints, "policies": map[string]egress.EndpointList{"pi": {Endpoints: catalog.Policies["pi"].Endpoints, Direct: catalog.Policies["pi"].Direct}}},
		"max_active":    2, "inventory_limit": 10000, "lease_ttl": "1m", "poll_interval": "50ms", "event_interval": "50ms", "action_timeout": "10s", "capture_window": "1m", "inventory_interval": "200ms", "idle_interval": "1s", "idle_threshold": "1m",
	}
	if err := os.WriteFile(deploymentPath, mustJSON(document), 0600); err != nil {
		t.Fatal(err)
	}
	deployment, err := LoadDeployment(deploymentPath)
	if err != nil {
		t.Fatal(err)
	}
	faults := &lostStartupReply{Backend: backend}
	options := deployment.Options(faults, remotes, owner.UUID)
	options.OnError = func(scope string, err error) { t.Logf("%s: %v", scope, err) }
	runtime, err := Attach(ctx, app, st, options)
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

	if fullFaults {
		// Drop a successful remote start reply. No HTTP retry or explicit Ensure
		// follows: background work must adopt it or finish verified cold recovery.
		faults.armed.Store(true)
		pending, err := runtime.Controller.Ensure(ctx, execution.Session{ID: session, Driver: "pi"}, store.GenerationLeaseOwner(owner.UUID))
		if !sandbox.Is(err, sandbox.Unavailable) {
			t.Fatalf("lost start reply: %v", err)
		}
		for {
			current, err := st.GetExecutionBinding(ctx, pending.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State == "accepted" {
				break
			}
			if current.State == "retired" {
				var stage string
				if err := st.DBForTest().QueryRow(`SELECT stage FROM execution_recoveries WHERE generation_id=?`, current.GenerationID).Scan(&stage); err == nil && stage == "complete" {
					break
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal("background startup did not converge")
			case <-time.After(100 * time.Millisecond):
			}
		}
		if faults.starts.Load() < 2 {
			t.Fatalf("startup retry count: %d", faults.starts.Load())
		}
	}
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
	verifyManagedNetworkBoundary(t, ctx, backend, st, session, origin, upstreamListener.Addr().String(), gatewayEndpoint.String(), allowedTCP.Addr().String(), allowedUDP.LocalAddr().String())
	// Authority was recorded per request, attributed to this turn.
	var authorized int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_requests WHERE turn_id=? AND endpoint_id='model' AND outcome IS NOT NULL`, turn).Scan(&authorized); err != nil || authorized == 0 {
		t.Fatalf("model requests were not authorized and settled: %d %v", authorized, err)
	}
	var active int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_route_bindings WHERE state='active' AND endpoint_id='model'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("composed route was not activated: %d %v", active, err)
	}

	if !fullFaults {
		return
	}
	// A gateway crash leaves a stale Unix socket. Its ordinary command must
	// reclaim it, and background activation must restore the retained binding.
	previousGateway := remotes.Status()[0].Instance
	if previousGateway == "" {
		t.Fatal("gateway process was not pinned")
	}
	if err := gatewayProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-gatewayExited
	gatewayProcess, gatewayExited = startGateway()
	for {
		status := remotes.Status()[0]
		if status.Ready && status.Instance != "" && status.Instance != previousGateway {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("gateway restart did not converge: %+v", status)
		case <-time.After(100 * time.Millisecond):
		}
	}
	restarted := sendIntegrationTurn(t, ctx, app, session, "continue after gateway restart")
	awaitIntegrationTurn(t, ctx, st, session, restarted)

	holdModel.Store(true)
	interrupted := sendIntegrationTurn(t, ctx, app, session, "hold until interrupted")
	select {
	case <-modelStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session+"/interrupt", nil).WithContext(ctx)
	stopResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(stopResponse, stopRequest)
	if stopResponse.Code != http.StatusAccepted && !(stopResponse.Code == http.StatusServiceUnavailable && strings.Contains(stopResponse.Body.String(), "control_delivery_pending")) {
		t.Fatalf("managed interrupt: %d %s", stopResponse.Code, stopResponse.Body)
	}
	select {
	case <-modelClosed:
	case <-ctx.Done():
		t.Fatal("interrupt did not close upstream transport")
	}
	holdModel.Store(false)
	awaitIntegrationTurnStatus(t, ctx, st, session, interrupted, "canceled")

	// Active streams keep their original grant deadline when the application
	// authorization service disappears; the proxy's own timeout is longer.
	holdModel.Store(true)
	expiredTurn := sendIntegrationTurn(t, ctx, app, session, "hold until original grant expires")
	select {
	case <-modelStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var deadlineText string
	if err := st.DBForTest().QueryRow(`SELECT authorized_until FROM egress_requests WHERE turn_id=? ORDER BY started_at DESC LIMIT 1`, expiredTurn).Scan(&deadlineText); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, deadlineText)
	if err != nil {
		t.Fatal(err)
	}
	_ = authorizationServer.Close()
	<-authorizationDone
	authorizationRunning = false
	timer := time.NewTimer(time.Until(expires) + 5*time.Second)
	select {
	case <-modelClosed:
		if time.Now().Before(expires.Add(-time.Second)) {
			t.Fatal("stream closed before grant expiry was exercised")
		}
	case <-timer.C:
		t.Fatal("active stream outlived its grant during authorization outage")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timer.Stop()
	holdModel.Store(false)
	authorizationServer, authorizationDone = startAuthorization()
	authorizationRunning = true
	awaitIntegrationTurnStatus(t, ctx, st, session, expiredTurn, "failed")
	var unfinished int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_requests WHERE turn_id=? AND outcome IS NULL`, expiredTurn).Scan(&unfinished); err != nil || unfinished != 0 {
		t.Fatalf("lost finish report did not settle through transport drain: %d %v", unfinished, err)
	}
	t.Logf("grant expiry closed real steered transport during authorization outage at %s", expires.Format(time.RFC3339))

	// Upstream authentication is a request failure, never network authority.
	rejectAuthentication.Store(true)
	rejected := sendIntegrationTurn(t, ctx, app, session, "exercise upstream rejection")
	for {
		status, reason, err := st.TurnStatus(ctx, session, rejected)
		if err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			break
		}
		if status == "completed" || status == "canceled" {
			t.Fatalf("authentication rejection status %s: %s", status, reason)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	var rejectedRequests int
	if err := st.DBForTest().QueryRow(`SELECT count(*) FROM egress_requests WHERE turn_id=? AND outcome='upstream_401'`, rejected).Scan(&rejectedRequests); err != nil || rejectedRequests == 0 {
		t.Fatalf("authentication failure not settled: %d %v", rejectedRequests, err)
	}
	rejectAuthentication.Store(false)
	recovered := sendIntegrationTurn(t, ctx, app, session, "continue after authentication failure")
	awaitIntegrationTurn(t, ctx, st, session, recovered)

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
	// Every retained generation receives a distinct network declaration even when
	// the authored Pi plan is unchanged. Native conversation survives each pause.
	for iteration := 0; iteration < 2; iteration++ {
		turn := sendIntegrationTurn(t, ctx, app, session, "continue retained conversation")
		awaitIntegrationTurn(t, ctx, st, session, turn)
		next, found, err := st.FindActiveExecutionBinding(ctx, session)
		if err != nil || !found || next.GenerationID == binding.GenerationID {
			t.Fatalf("retained generation: %+v %v", next, err)
		}
		if err := runtime.Controller.ReleaseIdle(ctx, session, next.GenerationID, store.GenerationLeaseOwner(owner.UUID)); err != nil {
			t.Fatal(err)
		}
	}
	var destinations, generations int
	if err := st.DBForTest().QueryRow(`SELECT count(DISTINCT destination),count(DISTINCT generation_id) FROM egress_route_bindings`).Scan(&destinations, &generations); err != nil || destinations < 3 || destinations != generations {
		t.Fatalf("repeated generations did not retain destination identities: %d/%d %v", destinations, generations, err)
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
	awaitIntegrationTurnStatus(t, ctx, st, session, turn, "completed")
}
func awaitIntegrationTurnStatus(t *testing.T, ctx context.Context, st *store.Store, session string, turn int64, want string) {
	t.Helper()
	for {
		status, reason, err := st.TurnStatus(ctx, session, turn)
		if err != nil {
			t.Fatal(err)
		}
		if status == want {
			return
		}
		if status == "completed" || status == "failed" || status == "canceled" {
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

// Include the durable fence facts when real ingress cannot resolve readiness.
type routeDiagnostics struct {
	*store.Store
	t *testing.T
}

func (d routeDiagnostics) ResolveEgressRoute(ctx context.Context, p egress.RouteRequest) (egress.RouteIdentity, error) {
	route, err := d.Store.ResolveEgressRoute(ctx, p)
	if err != nil {
		rows, queryErr := d.DBForTest().QueryContext(ctx, `SELECT json_object('route',r.binding_id,'route_state',r.state,'route_epoch',r.epoch,'route_generation',r.generation_id,'binding_state',b.state,'binding_epoch',b.epoch,'binding_fence',b.environment_fence,'environment_epoch',e.epoch,'environment_fence',e.fence,'generation_status',g.status,'lease',g.lease_expires_at,'active_generation',s.active_generation_id,'session_status',s.status,'pending_ops',(SELECT count(*) FROM environment_operations o WHERE o.environment_id=e.environment_id AND o.state IN ('pending','issued','uncertain'))) FROM egress_route_bindings r LEFT JOIN execution_bindings b ON b.generation_id=r.generation_id LEFT JOIN sandbox_environments e ON e.environment_id=b.environment_id LEFT JOIN runtime_generations g ON g.generation_id=b.generation_id LEFT JOIN sessions s ON s.id=b.session_id`)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var facts string
				if rows.Scan(&facts) == nil {
					d.t.Logf("readiness facts: %s", facts)
				}
			}
		} else {
			d.t.Log(queryErr)
		}
	}
	return route, err
}

// The engine performs the start; only its reply is lost to the application.
type lostStartupReply struct {
	Backend
	armed  atomic.Bool
	starts atomic.Int64
}

func (b *lostStartupReply) StartProcess(ctx context.Context, p sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	stream, err := b.Backend.StartProcess(ctx, p)
	if strings.HasPrefix(p.Handle, "bridge_gen_") {
		b.starts.Add(1)
		if err == nil && b.armed.CompareAndSwap(true, false) {
			_ = stream.Close()
			return nil, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("injected loss of successful process-start reply")}
		}
	}
	return stream, err
}

func verifyManagedNetworkBoundary(t *testing.T, ctx context.Context, backend Backend, st *store.Store, session, origin, upstream, nextHop, allowedTCP, allowedUDP string) {
	t.Helper()
	binding, found, err := st.FindActiveExecutionBinding(ctx, session)
	if err != nil || !found {
		t.Fatalf("network probe binding: %v", err)
	}
	script := `import json,socket,urllib.request,urllib.error
origin,upstream,next_hop,allowed_tcp,allowed_udp=json.loads(` + string(mustJSON(string(mustJSON([]string{origin, upstream, nextHop, allowedTCP, allowedUDP})))) + `)
result={}
host,port=allowed_tcp.rsplit(':',1)
with socket.create_connection((host,int(port)),timeout=2) as connection:
 result['allowed_tcp']='allowed' if connection.recv(64)==b'direct-ok' else 'invalid'
host,port=allowed_udp.rsplit(':',1)
with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as connection:
 connection.settimeout(2)
 connection.sendto(b'udp-ok',(host,int(port)))
 result['allowed_udp']='allowed' if connection.recvfrom(64)[0]==b'udp-ok' else 'invalid'
for key,address in [('direct_upstream',upstream),('direct_gateway',next_hop),('undeclared','1.1.1.1:443')]:
 host,port=address.rsplit(':',1)
 try:
  connection=socket.create_connection((host,int(port)),timeout=1)
  connection.close()
  result[key]='connected'
 except OSError:
  result[key]='denied'
with urllib.request.urlopen(origin+'/healthz',timeout=3) as response:
 result['health']=response.status
request=urllib.request.Request(origin+'/v1/messages',data=b'{"model":"approved-model","messages":[]}',headers={'Content-Type':'application/json'})
try:
 with urllib.request.urlopen(request,timeout=3) as response: result['without_grant']=response.status
except urllib.error.HTTPError as error: result['without_grant']=error.code
print(json.dumps(result))
`
	action, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := backend.StartProcess(action, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch, Handle: "network-boundary-" + binding.GenerationID}, IdempotencyKey: "network-boundary-" + binding.GenerationID, Command: "/usr/bin/python3", Arguments: []string{"-c", script}, Directory: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var output strings.Builder
	for {
		frame, err := stream.Next()
		if err != nil {
			t.Fatalf("network boundary probe: %v %s", err, output.String())
		}
		if frame.Kind == "data" {
			if output.Len()+len(frame.Data) > 8192 {
				t.Fatal("network probe output exceeded bound")
			}
			output.Write(frame.Data)
		}
		if frame.Kind == "end" {
			if frame.ExitCode != 0 {
				t.Fatalf("network probe exit %d: %s", frame.ExitCode, output.String())
			}
			break
		}
	}
	var observed struct {
		AllowedTCP     string `json:"allowed_tcp"`
		AllowedUDP     string `json:"allowed_udp"`
		Health         int
		WithoutGrant   int    `json:"without_grant"`
		DirectUpstream string `json:"direct_upstream"`
		DirectGateway  string `json:"direct_gateway"`
		Undeclared     string
	}
	if err := json.Unmarshal([]byte(output.String()), &observed); err != nil {
		t.Fatal(err)
	}
	if observed.AllowedTCP != "allowed" || observed.AllowedUDP != "allowed" || observed.Health != 200 || observed.WithoutGrant != 403 || observed.DirectUpstream != "denied" || observed.DirectGateway != "denied" || observed.Undeclared != "denied" {
		t.Fatalf("network authority bypass: %+v", observed)
	}
	t.Logf("real network boundary: %s", strings.TrimSpace(output.String()))
}
