//go:build khoraintegration

// This is an external test package on purpose: the acceptance run reaches the
// Project only through its exported application API, the way a deployment does.
package servicegateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/servicegateway"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// viaIdentity is the engine's configured second-hop UID. The engine refuses an
// egress.via declaration without one, because only a UID can pin the gateway
// process's own egress, so this is a deployment requirement, not scaffolding.
func viaIdentity(t *testing.T) (int, int) {
	t.Helper()
	uid, uidErr := strconv.Atoi(os.Getenv("PROJECT_TEST_VIA_UID"))
	gid, gidErr := strconv.Atoi(os.Getenv("PROJECT_TEST_VIA_GID"))
	if uidErr != nil || gidErr != nil || uid <= 0 {
		t.Fatal("isolated PROJECT_TEST_VIA_UID and PROJECT_TEST_VIA_GID required")
	}
	return uid, gid
}

// hostEndpoint is the address the guest steers to. It is never loopback: the
// guest's loopback is its own, and the engine refuses a loopback second hop.
func hostEndpoint(t *testing.T) netip.Addr {
	t.Helper()
	interfaces, err := net.Interfaces()
	must(t, err)
	for _, candidate := range interfaces {
		if candidate.Flags&net.FlagLoopback != 0 || candidate.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := candidate.Addrs()
		must(t, err)
		for _, address := range addresses {
			prefix, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			if parsed, ok := netip.AddrFromSlice(prefix.IP.To4()); ok && parsed.Is4() && !parsed.IsLoopback() {
				return parsed
			}
		}
	}
	t.Fatal("isolated harness must provide a non-loopback host address")
	return netip.Addr{}
}

const guestModels = `{"providers":{"gateway":{"baseUrl":"http://model.internal:443","api":"anthropic-messages","apiKey":"harness-placeholder","models":[{"id":"local-model","name":"Local model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`

func TestProjectServiceAuthorityOnKhora(t *testing.T) {
	endpoint, binaries := os.Getenv("PROJECT_TEST_ENDPOINT"), os.Getenv("PROJECT_TEST_BIN_DIR")
	if endpoint == "" || binaries == "" {
		t.Fatal("isolated PROJECT_TEST_ENDPOINT and PROJECT_TEST_BIN_DIR required")
	}
	viaUID, viaGID := viaIdentity(t)
	runctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	api, err := sdk.New(endpoint)
	must(t, err)
	for i := 0; i < 150; i++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	host := hostEndpoint(t)

	// The upstream holds the only real secret and runs under the gateway's UID, so
	// neither the application nor the guest can read it. It records the
	// host-derived attribution of every request it serves, and holds each Session's
	// first request until the other has arrived, so a completed pair of turns
	// proves concurrency rather than incidental ordering.
	upstreamRoot, err := os.MkdirTemp("/run", "project-upstream-")
	if err != nil {
		t.Fatalf("socket-capable runtime directory required: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(upstreamRoot) })
	must(t, os.Chmod(upstreamRoot, 0o755))
	fixtureSource, err := os.ReadFile(filepath.Join(binaries, "runtime-fixture"))
	must(t, err)
	fixture := filepath.Join(upstreamRoot, "runtime-fixture")
	must(t, os.WriteFile(fixture, fixtureSource, 0o755))
	report := filepath.Join(upstreamRoot, "attribution.jsonl")
	must(t, os.WriteFile(report, nil, 0o666))
	// The upstream runs under the gateway UID, so its report must be writable by
	// that UID regardless of this process's umask.
	must(t, os.Chown(report, viaUID, viaGID))
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	upstreamAddress := probe.Addr().String()
	must(t, probe.Close())
	upstream := exec.CommandContext(runctx, fixture, "upstream")
	upstream.Env = append(os.Environ(), "PROJECT_UPSTREAM_ADDR="+upstreamAddress, "PROJECT_UPSTREAM_REPORT="+report, "PROJECT_UPSTREAM_SECRET=fixture-upstream-secret", "PROJECT_UPSTREAM_OVERLAP=2")
	upstream.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(viaUID), Gid: uint32(viaGID)}}
	upstreamOut, err := upstream.StdoutPipe()
	must(t, err)
	upstream.Stderr = os.Stderr
	must(t, upstream.Start())
	t.Cleanup(func() { _ = upstream.Process.Kill(); _ = upstream.Wait() })
	if _, err = bufio.NewReader(upstreamOut).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	attributions := func(t *testing.T, session string) []map[string]string {
		t.Helper()
		raw, err := os.ReadFile(report)
		must(t, err)
		var out []map[string]string
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var entry map[string]string
			if line == "" || json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			if session == "" || entry["session"] == session {
				out = append(out, entry)
			}
		}
		return out
	}

	gatewayListener, err := net.Listen("tcp", net.JoinHostPort(host.String(), "0"))
	must(t, err)
	gatewayPort := uint16(gatewayListener.Addr().(*net.TCPAddr).Port)

	store, err := product.Open(runctx, filepath.Join(t.TempDir(), "product.db"))
	must(t, err)
	t.Cleanup(func() { store.Close() })
	lifecycle := &product.Lifecycle{Store: store, Backend: backend, MaxProjects: 1}
	scope := product.Scope{Actor: "owner", ProjectID: "services"}
	_, err = store.CreateProject(runctx, scope.Actor, scope.ProjectID, "Services")
	must(t, err)
	synthetic := netip.MustParseAddr("10.80.0.1")
	profile := product.Profile{
		StoreID: identity.StoreID, TemplateID: "battery",
		CPUMillis: 2000, MemoryBytes: 2 << 30, DiskBytes: 256 << 20,
		WorkspaceBytes: 64 << 20, RuntimeBytes: 64 << 20,
		Reachability: product.Reachability{
			Services: []product.ServiceRule{{Audience: "model", Destination: synthetic, Port: 443, Gateway: netip.AddrPortFrom(host, gatewayPort)}},
			Hosts:    map[string]string{"model.internal": "model"},
		},
	}
	must(t, lifecycle.EnsureActive(runctx, scope, profile))
	converge := func(t *testing.T) {
		t.Helper()
		for i := 0; i < 24; i++ {
			err := lifecycle.Reconcile(runctx, scope.ProjectID)
			if errors.Is(err, product.ErrNotFound) {
				return
			}
			must(t, err)
		}
		t.Fatal("Project lifecycle did not converge")
	}
	converge(t)
	resources, err := lifecycle.ActiveResources(runctx, scope)
	must(t, err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		p, e := store.Project(c, scope)
		if e == nil && p.State != "deleted" {
			if p.State != "deleting" {
				_ = store.DeleteProject(c, scope, p.Revision)
			}
			for i := 0; i < 16; i++ {
				_ = lifecycle.Reconcile(c, scope.ProjectID)
			}
		}
	})

	newGateway := func(t *testing.T, listener net.Listener) func() {
		t.Helper()
		gateway, err := servicegateway.New(store, servicegateway.Config{Audience: "model", Upstream: "http://" + upstreamAddress, APIKey: "fixture-upstream-secret", MaxRequests: 8, RecheckInterval: 20 * time.Millisecond})
		must(t, err)
		server := &http.Server{Handler: gateway}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		var once sync.Once
		return func() { once.Do(func() { server.Close(); <-done; gateway.Close() }) }
	}
	stopGateway := newGateway(t, gatewayListener)
	defer stopGateway()
	captures := &product.Captures{Store: store, Backend: backend, Directory: t.TempDir()}

	serial := 0
	shell := func(t *testing.T, command string) (string, int32) {
		t.Helper()
		serial++
		handle := fmt.Sprintf("probe-%d", serial)
		ref := sandbox.ProcessReference{StoreID: resources.Reference.StoreID, SandboxID: resources.Reference.SandboxID, Epoch: resources.Reference.Epoch, Handle: handle}
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
		must(t, err)
		defer stream.Close()
		var out strings.Builder
		for {
			frame, err := stream.Next()
			must(t, err)
			out.Write(frame.Data)
			if frame.Kind == "end" {
				return out.String(), frame.ExitCode
			}
		}
	}

	// The lifecycle already materialized this Project's reachability for this
	// placement, before any child existed. That is the only thing giving the guest
	// a stable name for the synthetic destination.
	if out, _ := shell(t, "cat /etc/hosts"); !strings.Contains(out, synthetic.String()+"\tmodel.internal") {
		t.Fatalf("Project reachability projection missing: %q", out)
	}
	t.Log("Project reachability was materialized once by the lifecycle, before any child started")

	agents := product.NewAgents(runctx, store, lifecycle, backend, profile, 8)
	agents.RunnerBinary = "/tmp/agent-runner"
	agents.ServiceAudience = "model"
	closeAgents := sync.OnceFunc(agents.Close)
	defer closeAgents()
	file, err := os.Open(filepath.Join(binaries, "agent-runner"))
	must(t, err)
	must(t, api.WriteFile(runctx, resources.Reference.SandboxID, "/tmp/agent-runner", file))
	file.Close()
	shell(t, "chmod 755 /tmp/agent-runner")
	session := func(t *testing.T, id string) {
		t.Helper()
		_, err := store.CreateSession(runctx, scope, id, "Agent")
		must(t, err)
		shell(t, "mkdir -p /runtime/sessions/"+id)
		must(t, api.WriteFile(runctx, resources.Reference.SandboxID, "/runtime/sessions/"+id+"/models.json", strings.NewReader(guestModels)))
		must(t, api.WriteFile(runctx, resources.Reference.SandboxID, "/runtime/sessions/"+id+"/settings.json", strings.NewReader(`{"defaultProvider":"gateway","defaultModel":"local-model","compaction":{"enabled":false}}`)))
	}
	// step drives the application's own worker path. It returns when each named
	// Session has the expected number of completed turns.
	step := func(t *testing.T, want int, ids ...string) {
		t.Helper()
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			done := 0
			for _, id := range ids {
				err := agents.Step(runctx, scope, id)
				if err != nil && !errors.Is(err, product.ErrPending) && !errors.Is(err, product.ErrNotFound) && !errors.Is(err, product.ErrConflict) {
					t.Fatal(id, err)
				}
				turns, err := store.Turns(runctx, scope, id, 0, 20)
				must(t, err)
				completed := 0
				for _, turn := range turns {
					if turn.State == "completed" {
						completed++
					}
				}
				if completed >= want {
					done++
				}
			}
			if done == len(ids) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, id := range ids {
			turns, _ := store.Turns(runctx, scope, id, 0, 20)
			t.Logf("%s turns: %+v", id, turns)
		}
		t.Fatal("turns did not complete through the Project gateway")
	}

	for _, id := range []string{"a", "b"} {
		session(t, id)
		_, err = store.AdmitTurn(runctx, scope, id, "first", "hello-"+id, false)
		must(t, err)
	}
	step(t, 1, "a", "b")
	first := map[string]map[string]string{}
	for _, id := range []string{"a", "b"} {
		entries := attributions(t, id)
		if len(entries) == 0 {
			turns, _ := store.Turns(runctx, scope, id, 0, 20)
			t.Fatalf("%s never reached the upstream; turns %+v, all attributions %+v", id, turns, attributions(t, ""))
		}
		first[id] = entries[0]
		turns, err := store.Turns(runctx, scope, id, 0, 20)
		must(t, err)
		messages, err := store.Messages(runctx, scope, id, 0, 20)
		must(t, err)
		if len(messages) != 2 || messages[1].Content != "deterministic-local-reply" {
			t.Fatalf("%s: %+v", id, messages)
		}
		if first[id]["project"] != scope.ProjectID || first[id]["session"] != id || first[id]["turn"] != turns[0].ID || first[id]["incarnation"] == "" {
			t.Fatalf("%s attributed to %+v, want its own turn %s", id, first[id], turns[0].ID)
		}
	}
	if first["a"]["turn"] == first["b"]["turn"] || first["a"]["incarnation"] == first["b"]["incarnation"] {
		t.Fatal("concurrent Sessions shared an attribution", first)
	}
	// The guest's authored configuration holds an application grant, never the
	// upstream secret, and one Session's credential differs from its sibling's.
	credential := func(t *testing.T, id string) string {
		t.Helper()
		out, _ := shell(t, `python3 -c 'import json;print(json.load(open("/runtime/sessions/`+id+`/models.json"))["providers"]["gateway"]["apiKey"])'`)
		return strings.TrimSpace(out)
	}
	tokenA, tokenB := credential(t, "a"), credential(t, "b")
	if tokenA == "" || tokenA == tokenB || strings.Contains(tokenA+tokenB, "fixture-upstream-secret") || strings.Contains(tokenA+tokenB, "harness-placeholder") {
		t.Fatalf("guest credentials are wrong: %q %q", tokenA, tokenB)
	}
	t.Log("two real Pi Sessions completed overlapping turns through one Project gateway, each attributed to its own Session, incarnation and turn, with the upstream secret held only by the gateway")

	// A raw guest request, with forged attribution headers, exercises authority
	// directly rather than through Pi.
	request := func(t *testing.T, token string, timeout int) (int, string) {
		t.Helper()
		out, _ := shell(t, fmt.Sprintf(`curl -s -o /tmp/body -w '%%{http_code}' --max-time %d -X POST -H 'Authorization: Bearer %s' -H 'X-Session-Id: forged' -H 'X-Turn-Id: forged' -H 'X-Project-Id: forged' -d '{}' 'http://model.internal:443/v1/messages'; printf ' '; head -c 200 /tmp/body 2>/dev/null`, timeout, token))
		fields := strings.SplitN(strings.TrimSpace(out), " ", 2)
		code, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("guest request produced no status: %q", out)
		}
		if len(fields) == 2 {
			return code, fields[1]
		}
		return code, ""
	}
	// Both turns have completed, so neither Session has service authority now: the
	// grant survives, but authority requires an admitted turn.
	for id, token := range map[string]string{"a": tokenA, "b": tokenB} {
		if code, _ := request(t, token, 20); code != 401 {
			t.Fatalf("%s reached the upstream with no admitted turn: %d", id, code)
		}
	}
	grants, err := store.ServiceGrants(runctx, scope)
	must(t, err)
	if len(grants) != 2 {
		t.Fatalf("turn completion revoked incarnation grants: %+v", grants)
	}
	if forged, _ := request(t, "not-a-grant.0000", 10); forged != 401 {
		t.Fatal("a forged credential was accepted")
	}
	if mutated, _ := request(t, tokenA[:len(tokenA)-1]+"0", 10); mutated != 401 {
		t.Fatal("a mutated credential was accepted")
	}
	t.Log("an idle agent holding a valid credential has no service authority; forged and mutated credentials are refused")

	// A new turn on Session A is a fresh attribution under the same grant, and B is
	// undisturbed. This is the stop-one/continue-other property after a completion.
	next, err := store.AdmitTurn(runctx, scope, "a", "second", "again", false)
	must(t, err)
	step(t, 2, "a")
	successor := attributions(t, "a")
	if len(successor) < 2 || successor[len(successor)-1]["turn"] != next.ID {
		t.Fatalf("successor turn was not attributed to %s: %+v", next.ID, successor)
	}
	if successor[len(successor)-1]["turn"] == first["a"]["turn"] {
		t.Fatal("successor request inherited the completed turn's attribution")
	}
	turnsB, err := store.Turns(runctx, scope, "b", 0, 20)
	must(t, err)
	if turnsB[0].State != "completed" || len(turnsB) != 1 {
		t.Fatal("sibling Session was disturbed", turnsB)
	}
	t.Log("a successor turn on the same Session is a fresh attribution, never an inheritance of the completed one")

	// Authority is durable: a gateway process holds none of its own. B completes a
	// real turn through the replacement process.
	stopGateway()
	if code, _ := request(t, tokenB, 10); code == 200 {
		t.Fatal("a stopped gateway still served traffic")
	}
	replacementListener, err := net.Listen("tcp", net.JoinHostPort(host.String(), strconv.Itoa(int(gatewayPort))))
	must(t, err)
	stopReplacement := newGateway(t, replacementListener)
	defer stopReplacement()
	_, err = store.AdmitTurn(runctx, scope, "b", "second", "still here", false)
	must(t, err)
	step(t, 2, "b")
	t.Log("gateway restart preserved durable authority: the product store is the only authority")

	// Default-deny holds for everything the Project did not declare, including for
	// a Terminal-style process that carries no credential at all.
	if out, code := shell(t, `curl -s --max-time 5 -o /dev/null -w '%{http_code}' http://`+upstreamAddress+`/v1/messages; echo`); code == 0 && strings.HasPrefix(strings.TrimSpace(out), "2") {
		t.Fatalf("guest reached the upstream directly, bypassing attribution: %q", out)
	}
	if _, code := shell(t, `curl -s --max-time 5 -o /dev/null http://203.0.113.9:443/`); code == 0 {
		t.Fatal("an undeclared destination was reachable")
	}
	t.Log("default-deny holds for undeclared destinations and for direct upstream access")

	// Project restart revokes every grant and re-materializes the same immutable
	// reachability for the successor placement. Explicit restart rejects admitted
	// activity, so the Sessions are ended first — the runner's own descendant
	// settlement is what clears the blockers, not a forced state write.
	for _, id := range []string{"a", "b"} {
		child, err := store.Session(runctx, scope, id)
		must(t, err)
		must(t, store.DeleteSession(runctx, scope, id, child.Revision))
	}
	quiesced := false
	for i := 0; i < 120 && !quiesced; i++ {
		blockers, err := store.Blockers(runctx, scope)
		must(t, err)
		quiesced = len(blockers) == 0
		if !quiesced {
			// Drive the child cleanup the deletion requested, using the same exported
			// operations a composed worker uses: stop the runner, then settle the child
			// once its termination evidence and activity allow it.
			// Completed native bytes must leave the sandbox before it does, so the
			// capture worker runs alongside child cleanup.
			pendingCaptures, err := store.PendingCaptures(runctx, 16)
			must(t, err)
			for _, capture := range pendingCaptures {
				must(t, captures.Export(runctx, capture))
			}
			children, err := store.PendingChildren(runctx, 16)
			must(t, err)
			for _, child := range children {
				if child.Kind == "session" {
					if err := agents.Stop(runctx, child.ProjectID, child.ID); err != nil && !errors.Is(err, product.ErrNotFound) {
						t.Fatal(err)
					}
				}
				if err := store.SettleChild(runctx, child); err != nil && !errors.Is(err, product.ErrBusy) {
					t.Fatal(err)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !quiesced {
		blockers, _ := store.Blockers(runctx, scope)
		children, _ := store.PendingChildren(runctx, 16)
		var reasons []string
		for _, child := range children {
			reasons = append(reasons, fmt.Sprintf("%s: %v", child.ID, store.SettleChild(runctx, child)))
		}
		t.Fatalf("Project did not quiesce for an explicit restart: %+v\nsettle: %v", blockers, reasons)
	}
	closeAgents()
	grants, err = store.ServiceGrants(runctx, scope)
	must(t, err)
	if len(grants) != 0 {
		t.Fatal("ending a Session left its grant live", grants)
	}
	p, err := store.Project(runctx, scope)
	must(t, err)
	must(t, lifecycle.Restart(runctx, scope, p.Revision))
	converge(t)
	resumed, err := lifecycle.ActiveResources(runctx, scope)
	must(t, err)
	if resumed.Reference.Epoch <= resources.Reference.Epoch {
		t.Fatal(resources.Reference, resumed.Reference)
	}
	grants, err = store.ServiceGrants(runctx, scope)
	must(t, err)
	if len(grants) != 0 {
		t.Fatal("Project restart left live grants", grants)
	}
	resources = resumed
	if out, _ := shell(t, "cat /etc/hosts"); !strings.Contains(out, synthetic.String()+"\tmodel.internal") {
		t.Fatalf("resumed placement lost its reachability projection: %q", out)
	}
	if code, _ := request(t, tokenA, 10); code != 401 {
		t.Fatal("a credential survived Project restart")
	}
	t.Log("Project restart revoked every grant and re-materialized the same reachability under the successor placement")

	p, err = store.Project(runctx, scope)
	must(t, err)
	must(t, store.DeleteProject(runctx, scope, p.Revision))
	converge(t)
	if _, err = api.Get(runctx, resumed.Reference.SandboxID); !sdk.IsNotFound(err) {
		t.Fatalf("sandbox not reclaimed: %v", err)
	}
	t.Log("Project deletion closed service authority and reclaimed the sandbox")
}
