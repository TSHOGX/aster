//go:build khoraintegration

package application_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

// This gate exercises actual guest DNS, TLS/package bytes, denial, binding
// replacement and retained model routing on the isolated disposable substrate.
// It is deliberately independent of public Internet availability.
func TestReleasedNetworkPolicyReload(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	socket := filepath.Join(a.root, "state/control.sock")
	backend, err := khora.New(a.ctx, khora.Config{Endpoint: os.Getenv("PROJECT_TEST_ENDPOINT")})
	require(t, err)
	dns, err := net.ListenPacket("udp", "192.0.2.1:53")
	require(t, err)
	defer dns.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, e := dns.ReadFrom(buf)
			if e != nil {
				return
			}
			if n < 12 {
				continue
			}
			end := 12
			for end < n && buf[end] != 0 {
				end += 1 + int(buf[end])
			}
			end += 5
			if end > n {
				continue
			}
			reply := append([]byte{}, buf[:end]...)
			binary.BigEndian.PutUint16(reply[2:4], 0x8180)
			binary.BigEndian.PutUint16(reply[6:8], 1)
			reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 192, 0, 2, 1)
			_, _ = dns.WriteTo(reply, addr)
		}
	}()
	payload := bytes.Repeat([]byte("isolated-package-fixture\n"), 4096)
	tls := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(payload)
	}))
	listener, err := net.Listen("tcp", "192.0.2.1:0")
	require(t, err)
	tls.Listener = listener
	tls.StartTLS()
	defer tls.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	certificate := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tls.Certificate().Raw}))
	base := "/api/projects/network"
	a.request("POST", "/api/projects", map[string]string{"id": "network", "title": "Network reload gate"}, 201)
	a.request("POST", base+"/resume", nil, 202)
	a.await(25*time.Second, "initial active placement", func() bool { return a.request("GET", base, nil, 200)["state"] == "active" })
	inspect := func() (sandbox.Reference, map[string]any) {
		t.Helper()
		d := a.cli("diagnostics", "--socket", socket, "--project", "network")
		raw, _ := json.Marshal(d["resources"].(map[string]any)["reference"])
		var ref sandbox.Reference
		require(t, json.Unmarshal(raw, &ref))
		return ref, d
	}
	ref, original := inspect()
	sequence := 0
	shell := func(code string) string {
		t.Helper()
		sequence++
		handle := fmt.Sprintf("network-check-%d", sequence)
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		stream, err := backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: ref.StoreID, SandboxID: ref.SandboxID, Epoch: ref.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/usr/bin/python3", Arguments: []string{"-c", code}, Directory: "/workspace"})
		require(t, err)
		defer stream.Close()
		var output strings.Builder
		for {
			frame, err := stream.Next()
			require(t, err)
			if frame.Kind == "data" {
				output.Write(frame.Data)
			}
			if frame.Kind == "end" {
				if frame.ExitCode != 0 {
					t.Fatalf("guest check exit %d: %s", frame.ExitCode, output.String())
				}
				return output.String()
			}
		}
	}
	shell("open('/workspace/retained','w').write('network-retained'); assert open('/etc/resolv.conf').read()==''")
	raw, err := os.ReadFile(a.config)
	require(t, err)
	var cfg map[string]any
	require(t, json.Unmarshal(raw, &cfg))
	reach := cfg["project"].(map[string]any)["reachability"].(map[string]any)
	reach["resolvers"] = []string{"192.0.2.1"}
	reach["direct"] = []any{map[string]any{"cidr": "192.0.2.1/32", "port": port, "protocol": "tcp"}}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(cfg)
		require(t, err)
		require(t, os.WriteFile(a.config, raw, 0644))
	}
	write()
	accepted := a.cli("network-reload", "--socket", socket)["defaults"].(map[string]any)["revision"]
	same, current := inspect()
	if same != ref || current["network_policy_pending"] != true {
		t.Fatal("running placement changed", same, current)
	}
	shell("assert open('/etc/resolv.conf').read()==''")
	pauseResume := func() {
		t.Helper()
		p := a.request("GET", base, nil, 200)
		if p["state"] == "active" {
			a.request("POST", fmt.Sprintf("%s/pause?revision=%.0f", base, p["revision"]), nil, 202)
		}
		a.await(25*time.Second, "paused", func() bool { return a.request("GET", base, nil, 200)["state"] == "paused" })
		a.request("POST", base+"/resume", nil, 202)
		a.await(25*time.Second, "resumed", func() bool { return a.request("GET", base, nil, 200)["state"] == "active" })
		ref, current = inspect()
	}
	pauseResume()
	if current["effective_network_policy_revision"] != accepted || current["network_policy_pending"] != false || current["configuration_matches"] != true {
		t.Fatal(current)
	}
	if !reflect.DeepEqual(current["resources"].(map[string]any)["volumes"], original["resources"].(map[string]any)["volumes"]) {
		t.Fatal("saved volume slots changed")
	}

	code := fmt.Sprintf(`import socket,ssl,urllib.request,base64
assert open('/workspace/retained').read()=='network-retained'
assert socket.gethostbyname('example.com')=='192.0.2.1'
context=ssl.create_default_context(cadata=base64.b64decode('%s').decode())
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPSHandler(context=context))
data=opener.open('https://example.com:%d/package.whl',timeout=5).read()
assert len(data)==%d
assert open('/etc/hosts').read().find('model.internal')>=0
try:
 socket.create_connection(('192.0.2.1',18890),timeout=1)
except OSError: pass
else: raise Exception('undeclared upstream was reachable')
print('DNS TLS PACKAGE DENIAL OK')`, certificate, port, len(payload))
	if !strings.Contains(shell(code), "DNS TLS PACKAGE DENIAL OK") {
		t.Fatal("guest network check missing")
	}
	reach["direct"] = []any{}
	reach["resolvers"] = []string{}
	write()
	a.cli("network-reload", "--socket", socket)
	// The live placement still has the previously selected permission.
	shell(code)
	pauseResume()
	shell(fmt.Sprintf("import socket\nassert open('/etc/resolv.conf').read()==''\ntry:\n socket.create_connection(('192.0.2.1',%d),timeout=1)\nexcept OSError: pass\nelse: raise Exception('removed access remains')", port))
	// Retained service routes still support a real native turn after replacement.
	a.request("POST", base+"/sessions", map[string]string{"id": "model", "title": "Retained model route"}, 201)
	a.request("POST", base+"/sessions/model/turns", map[string]string{"request_id": "network-model", "content": "Reply briefly"}, 201)
	a.await(45*time.Second, "model completion after network replacement", func() bool {
		turns, _ := a.request("GET", base+"/sessions/model/turns", nil, 200)["turns"].([]any)
		return len(turns) == 1 && turns[0].(map[string]any)["state"] == "completed"
	})
	p := a.request("GET", base, nil, 200)
	a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", base, p["revision"]), nil, 202)
	a.await(25*time.Second, "network Project cleanup", func() bool {
		return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == float64(1)
	})
	report := map[string]any{"dns": true, "tls_package_bytes": len(payload), "undeclared_denied": true, "running_unchanged": true, "permission_removal": true, "retained_model_completed": true, "public_internet_tested": false, "destination": netip.MustParseAddr("192.0.2.1").String()}
	raw, _ = json.MarshalIndent(report, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "network-policy.json"), raw, 0644))
}
