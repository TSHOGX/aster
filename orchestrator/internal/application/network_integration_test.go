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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

// This gate exercises actual guest DNS, TLS/package bytes, denial, binding
// replacement and retained model routing on the isolated disposable substrate.
// It is deliberately independent of public Internet availability.
func TestReleasedRuntimePathsAndVersions(t *testing.T) {
	if os.Getenv("PROJECT_TEST_SECONDARY_MANIFEST") == "" {
		t.Fatal("second qualified runtime required")
	}
	TestReleasedDatabaseNetworkPolicy(t)
}

func TestReleasedDatabaseNetworkPolicy(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)

	for _, kind := range []string{"sandbox", "volume"} {
		inventory := a.request("GET", "/api/admin/runtime/options?kind="+kind+"&limit=20", nil, 200)
		options := inventory["options"].([]any)
		if len(options) == 0 {
			t.Fatal("runtime inventory missing", kind)
		}
		for _, raw := range options {
			option := raw.(map[string]any)
			if option["availability"] != "available" || option["checked_at"] == nil {
				t.Fatal("actual runtime dependency not observed", option)
			}
			if kind == "volume" && option["source_capacity_bytes"] != "67108864" {
				t.Fatal(option)
			}
			for _, private := range []string{"store_id", "template_id", "volume_id", "entries"} {
				if _, exists := option[private]; exists {
					t.Fatal("private binding exposed", option)
				}
			}
		}
	}
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
	workspace, runtimeRoot := "/workspaces/network", "/state/network"
	var nextRelease any
	deletedProjects := float64(1)
	editPaths := func(workspace, runtimeRoot, requestID string) {
		t.Helper()
		template := a.resourceTemplate("environment", "standard-agent")
		current := template["environment"].(map[string]any)
		mounts := current["mounts"].([]any)
		for _, raw := range mounts {
			mount := raw.(map[string]any)
			if mount["role"] == "workspace" {
				mount["destination"] = workspace
			}
			if mount["role"] == "runtime" {
				mount["destination"] = runtimeRoot
			}
		}
		if nextRelease != nil {
			current["sandbox_release"] = nextRelease
		}
		a.saveResourceTemplate("environment", template, current, requestID)
	}
	editPaths(workspace, runtimeRoot, "non-default-paths")
	base := "/api/projects/network"
	a.request("POST", "/api/projects", map[string]string{"id": "network", "title": "Network policy gate"}, 201)
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
	checkStorage := func(owned string) {
		t.Helper()
		storage := a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["storage"].(map[string]any)
		publications := storage["publications"].(map[string]any)
		// Two 64MiB system publications each retain staging and immutable derivative.
		if publications["used"] != "268435456" || publications["reserved"] != "0" || storage["published_bytes"] != "134217728" || storage["owned"].(map[string]any)["used"] != owned {
			t.Fatal(storage)
		}
	}
	checkStorage("268435456")

	sequence := 0
	shell := func(code string) string {
		t.Helper()
		sequence++
		handle := fmt.Sprintf("network-check-%d", sequence)
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		stream, err := backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: ref.StoreID, SandboxID: ref.SandboxID, Epoch: ref.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/usr/bin/python3", Arguments: []string{"-c", strings.ReplaceAll(code, "/workspace", workspace)}, Directory: workspace})
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
	// Both active file helper modes must use the selected role mount.
	a.writeFile(base+"/files/content?path=via-helper", "saved-path-file", "path-upload")
	response, err := a.client.Get(a.origin + base + "/files/content?path=" + url.QueryEscape("via-helper"))
	require(t, err)
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	require(t, err)
	if response.StatusCode != 200 || string(data) != "saved-path-file" {
		t.Fatal(response.StatusCode, string(data))
	}
	shell("assert open('/workspace/via-helper').read()=='saved-path-file'")
	if manifestPath := os.Getenv("PROJECT_TEST_SECONDARY_MANIFEST"); manifestPath != "" {
		raw, err := os.ReadFile(manifestPath)
		require(t, err)
		digest, err := runtimerelease.Digest(raw)
		require(t, err)
		registered := a.cli("runtime", "register", "--socket", socket, "--id", "agent-runtime", "--version", "v2", "--template", "agent-runtime-v2", "--manifest", manifestPath, "--digest", digest)
		nextRelease = registered["reference"]
		if registered["default_changed"] != false {
			t.Fatal(registered)
		}
		// Registration alone cannot select the new image.
		unchanged := a.resourceTemplate("environment", "standard-agent")["environment"].(map[string]any)
		if unchanged["sandbox_release"].(map[string]any)["digest"] == digest {
			t.Fatal("registration changed default")
		}
	}
	// A later default edit must not reinterpret the already-bound paths.
	editPaths("/workspaces/future", "/state/future", "future-paths")
	var originalRegistryArgs []string
	if nextRelease != nil {
		originalRelease := original["resources"].(map[string]any)["profile"].(map[string]any)["sandbox_release"].(map[string]any)
		originalRegistryArgs = []string{"--socket", socket, "--kind", "sandbox", "--id", originalRelease["id"].(string), "--version", originalRelease["version"].(string), "--digest", originalRelease["digest"].(string)}
		state := a.cli(append([]string{"runtime", "inspect"}, originalRegistryArgs...)...)
		if state["template_referenced"] != false || state["project_references"] != "1" {
			t.Fatal(state)
		}
		disableArgs := append(append([]string{"runtime", "disable"}, originalRegistryArgs...), "--expected-revision", fmt.Sprint(state["revision"]), "--request-id", "disable-retained-runtime")
		disabled := a.cli(disableArgs...)
		if disabled["enabled"] != false || disabled["revision"] != float64(2) {
			t.Fatal(disabled)
		}
		if replay := a.cli(disableArgs...); replay["replayed"] != true {
			t.Fatal(replay)
		}
		retireArgs := append(append([]string{"runtime", "retire"}, originalRegistryArgs...), "--expected-revision", "2", "--request-id", "retire-retained-runtime")
		if raw, err := exec.CommandContext(a.ctx, a.binary, retireArgs...).CombinedOutput(); err == nil || !strings.Contains(string(raw), "artifact_referenced") {
			t.Fatal(string(raw), err)
		}
		secondBase := "/api/projects/network-v2"
		a.request("POST", "/api/projects", map[string]string{"id": "network-v2", "title": "Second compatible runtime"}, 201)
		a.writeFile(secondBase+"/files/content?path=second-version", "second version bytes", "v2-first-upload")
		a.request("POST", secondBase+"/sessions", map[string]string{"id": "native-v2", "title": "Native on second runtime"}, 201)
		a.request("POST", secondBase+"/sessions/native-v2/turns", map[string]string{"request_id": "v2-native", "content": "Reply briefly"}, 201)
		a.await(45*time.Second, "second runtime native completion", func() bool {
			turns, _ := a.request("GET", secondBase+"/sessions/native-v2/turns", nil, 200)["turns"].([]any)
			return len(turns) == 1 && turns[0].(map[string]any)["state"] == "completed"
		})
		second := a.cli("diagnostics", "--socket", socket, "--project", "network-v2")
		secondResources := second["resources"].(map[string]any)
		secondProfile := secondResources["profile"].(map[string]any)
		if secondProfile["template_id"] != "agent-runtime-v2" || !reflect.DeepEqual(secondProfile["sandbox_release"], nextRelease) {
			t.Fatal(secondProfile)
		}
		response, err := a.client.Get(a.origin + secondBase + "/files/content?path=second-version")
		require(t, err)
		content, err := io.ReadAll(response.Body)
		response.Body.Close()
		require(t, err)
		if response.StatusCode != 200 || string(content) != "second version bytes" {
			t.Fatal(response.StatusCode, string(content))
		}
		// Both template bindings coexist. Removing v2 resources leaves v1 intact.
		secondProject := a.request("GET", secondBase, nil, 200)
		a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", secondBase, secondProject["revision"]), nil, 202)
		a.await(25*time.Second, "second runtime cleanup", func() bool {
			return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == float64(1)
		})
		deletedProjects = 2
		// Idle reconciliation may pause the first Project while v2 runs. Resume it
		// explicitly and prove the original release and role paths are retained.
		first := a.request("GET", base, nil, 200)
		if first["state"] != "active" {
			a.request("POST", base+"/resume", nil, 202)
			a.await(25*time.Second, "first runtime retained", func() bool { return a.request("GET", base, nil, 200)["state"] == "active" })
		}
		observed, current := inspect()
		ref = observed
		if !reflect.DeepEqual(current["resources"].(map[string]any)["profile"].(map[string]any)["sandbox_release"], original["resources"].(map[string]any)["profile"].(map[string]any)["sandbox_release"]) {
			t.Fatal("retained release rebound")
		}
	}

	raw, err := os.ReadFile(a.config)
	require(t, err)
	var cfg map[string]any
	require(t, json.Unmarshal(raw, &cfg))
	reach := cfg["project"].(map[string]any)["reachability"].(map[string]any)
	reach["resolvers"] = []string{"192.0.2.1"}
	reach["direct"] = []any{map[string]any{"cidr": "192.0.2.1/32", "port": port, "protocol": "tcp"}}
	customNetwork := a.copyFixtureNetwork()
	change := 0
	apply := func() any {
		t.Helper()
		change++
		current := a.resourceTemplate("network", customNetwork["id"].(string))
		result := a.saveResourceTemplate("network", current, map[string]any{"direct": reach["direct"], "resolvers": reach["resolvers"]}, fmt.Sprintf("network-change-%d", change))
		return result["network"].(map[string]any)["revision"]
	}
	accepted := apply()
	same, current := inspect()
	if same != ref || current["network_policy_pending"] != false {
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
		configurations := a.request("GET", "/api/me/project-configurations", nil, 200)["projects"].([]any)
		for _, item := range configurations {
			config := item.(map[string]any)
			if config["project_id"] == "network" {
				a.request("PATCH", "/api/me/project-configurations/network", map[string]any{"request_id": fmt.Sprintf("select-network-%d", change), "expected_revision": config["revision"], "network_template_id": customNetwork["id"]}, 200)
			}
		}
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
	// The deployment file still contains the bootstrap deny-all policy. An
	// ordinary owner restart must preserve the later database edit and binding.
	a.stop()
	a.start()
	restarted, retained := inspect()
	if restarted != ref || retained["desired_network_policy_revision"] != accepted || retained["effective_network_policy_revision"] != accepted {
		t.Fatal("restart replaced database network authority", retained)
	}
	shell(code)

	reach["direct"] = []any{map[string]any{"cidr": "0.0.0.0/0", "port": port, "protocol": "tcp"}}
	apply()
	pauseResume()
	shell(code)
	internetRef, _ := inspect()
	internetBinding, err := backend.InspectConfiguration(a.ctx, internetRef)
	require(t, err)
	for _, tuple := range internetBinding.Network.Allow {
		if tuple.CIDR.Bits() == 0 {
			t.Fatal("wildcard allow reached Khora", tuple)
		}
	}
	reach["direct"] = []any{}
	reach["resolvers"] = []string{}
	apply()
	_, pending := inspect()
	if pending["network_policy_pending"] != false || pending["network_policy_mode"] != "project" {
		t.Fatal(pending)
	}
	// The live placement still has the previously selected permission.
	shell(code)
	pauseResume()
	shell(fmt.Sprintf("import socket\nassert open('/etc/resolv.conf').read()==''\ntry:\n socket.create_connection(('192.0.2.1',%d),timeout=1)\nexcept OSError: pass\nelse: raise Exception('removed access remains')", port))

	// The saved project policy survives owner replacement. An explicit selection
	// applies the new template snapshot on the next verified resume.
	a.stop()
	a.start()
	_, overridden := inspect()
	if overridden["network_policy_pending"] != false || overridden["effective_network_policy_revision"] == accepted {
		t.Fatal(overridden)
	}
	reach["direct"] = []any{map[string]any{"cidr": "192.0.2.1/32", "port": port, "protocol": "tcp"}}
	reach["resolvers"] = []string{"192.0.2.1"}
	accepted = apply()
	shell("assert open('/etc/resolv.conf').read()==''")
	pauseResume()
	shell(code)
	if current["desired_network_policy_revision"] != accepted || current["network_policy_pending"] != false || !reflect.DeepEqual(current["resources"].(map[string]any)["volumes"], original["resources"].(map[string]any)["volumes"]) {
		t.Fatal(current)
	}
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
		return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == deletedProjects
	})
	checkStorage("0")
	if originalRegistryArgs != nil {
		retireArgs := append(append([]string{"runtime", "retire"}, originalRegistryArgs...), "--expected-revision", "2", "--request-id", "retire-retained-runtime")
		retired := a.cli(retireArgs...)
		if retired["retired_at"] == nil || retired["project_references"] != "0" || retired["pending_references"] != "0" {
			t.Fatal(retired)
		}
		if replay := a.cli(retireArgs...); replay["replayed"] != true {
			t.Fatal(replay)
		}
	}
	report := map[string]any{"dns": true, "tls_package_bytes": len(payload), "undeclared_denied": true, "running_unchanged": true, "permission_removal": true, "user_override_restart": true, "workspace_path": workspace, "runtime_path": runtimeRoot, "active_file_helpers": true, "second_release": nextRelease, "disabled_release_retained": originalRegistryArgs != nil, "registry_retired_after_cleanup": originalRegistryArgs != nil, "inheritance_restored": true, "retained_model_completed": true, "public_internet_tested": false, "destination": netip.MustParseAddr("192.0.2.1").String()}
	raw, _ = json.MarshalIndent(report, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "network-policy.json"), raw, 0644))
}
