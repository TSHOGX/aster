package retainedapp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/launchplan"
)

func deploymentFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	files := map[string]string{}
	for _, path := range []string{"/usr/sbin/sandboxd", "/usr/local/bin/harness-khora-launcher", "/usr/local/bin/harness-bridge-client", "/usr/local/bin/harness-shell-agent", "/usr/local/bin/harness-workspace-helper", "/usr/local/bin/harness-journal-replay", "/usr/local/lib/harness_files.py", "/usr/local/lib/harness_launch.py", "/usr/local/lib/harness_stream.py", "/usr/local/lib/harness_recovery.py"} {
		files[path] = digest
	}
	manifest := map[string]any{"schema_version": 1, "runtime": map[string]any{"backend": "khora", "engine_revision": launchplan.EngineRevision, "engine_module": launchplan.EngineModule, "bridge_stream_version": 1, "home": "/root", "retained_overlay": true, "launcher": "/usr/local/bin/harness-khora-launcher", "files": files}, "drivers": []any{map[string]any{"driver_id": "sh", "bridge_protocol": "harness_bridge_v2", "bridge_protocol_version": 2, "turn_input_schema": "RunTurn", "binary_path": "/usr/local/bin/harness-shell-agent", "installed_binary_digest": digest, "capabilities": []string{"process_stream", "filesystem", "durable_workspace", "cold_restart", "signal", "pty"}}}}
	raw, _ := json.Marshal(manifest)
	pin, err := launchplan.Digest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets", "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"assets/AGENTS.md": "shared context\n", "assets/skills/one.md": "step\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	value := map[string]any{"version": 1, "backend_name": "engine", "engine": map[string]any{"endpoint": "http://127.0.0.1:18191", "store_id": "pinned-store"}, "manifest": "manifest.json", "manifest_digest": pin, "prepared_root": "prepared", "authorization": map[string]any{"socket": "authorization.sock", "peer_uid": os.Getuid(), "group": os.Getgid()}, "gateways": []any{map[string]any{"id": "data", "socket": "data.sock", "peer_uid": os.Getuid()}}, "plans": map[string]any{"sh": map[string]any{"template": "battery", "cpu_millis": 1000, "memory_bytes": 1 << 30, "disk_bytes": 256 << 20, "workspace_bytes": 64 << 20, "journal_bytes": 64 << 20, "assets": "assets"}}, "content_bytes": 8 << 20, "max_active": 2, "inventory_limit": 1000, "lease_ttl": "1m", "poll_interval": "100ms", "event_interval": "1s", "action_timeout": "5s", "capture_window": "1m", "inventory_interval": "1s", "idle_interval": "1m", "idle_threshold": "30m"}
	return filepath.Join(root, "deployment.json"), value
}
func writeDeployment(t *testing.T, path string, value map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestDeploymentPinsReleaseAndResolvesPathsBesideFile(t *testing.T) {
	path, value := deploymentFixture(t)
	writeDeployment(t, path, value)
	deployment, err := LoadDeployment(path)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.PreparedRoot != filepath.Join(filepath.Dir(path), "prepared") || deployment.Gateways[0].Socket != filepath.Join(filepath.Dir(path), "data.sock") || deployment.Engine.StoreID != "pinned-store" {
		t.Fatalf("deployment identity/path mismatch: %+v", deployment)
	}
	if len(deployment.Content) != 1 || deployment.Content[0].Purpose != "assets" || !content.ValidDigest(deployment.Content[0].Revision.Digest) {
		t.Fatalf("authored content was not read from its reviewed tree: %+v", deployment.Content)
	}
	published := publishedContent{"pinned-store|assets|" + deployment.Content[0].Revision.Digest: "vol_assets"}
	bound, err := deployment.Plans.WithPublishedContent("pinned-store", published)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := bound.Plan(context.Background(), execution.Session{Driver: "sh"}, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Mounts) != 1 || plan.Mounts[0].Destination != "/root/.agents" || plan.Mounts[0].VolumeID != "vol_assets" || !strings.HasPrefix(plan.SpecDigest, "sha256:") {
		t.Fatalf("unpinned assets/plan: %+v", plan)
	}
	// The declared revision must be the authored tree's, not something the
	// deployment document could assert independently of the bytes.
	if plan.Content[0].RevisionDigest != deployment.Content[0].Revision.Digest {
		t.Fatalf("plan revision differs from authored content: %+v", plan.Content)
	}
}

type publishedContent map[string]string

func (p publishedContent) FindPublishedContent(_ context.Context, store, purpose, revision string) (string, bool, error) {
	volume, ok := p[store+"|"+purpose+"|"+revision]
	return volume, ok, nil
}

func TestDeploymentRefusesUnreadableAuthoredContent(t *testing.T) {
	path, value := deploymentFixture(t)
	plans := value["plans"].(map[string]any)["sh"].(map[string]any)
	plans["assets"] = "absent-assets"
	writeDeployment(t, path, value)
	if _, err := LoadDeployment(path); err == nil {
		t.Fatal("a missing authored content root was accepted")
	}
	path, value = deploymentFixture(t)
	value["content_bytes"] = 0
	writeDeployment(t, path, value)
	if _, err := LoadDeployment(path); err == nil {
		t.Fatal("a deployment without content capacity was accepted")
	}
}
func TestDeploymentRejectsImplicitCredentialsAndIncompleteBounds(t *testing.T) {
	for _, fault := range []string{"remote-http", "https-no-client", "engine-credentials", "missing-store", "unknown", "lease", "short-lease", "zero-cpu", "manifest", "gateway-uid", "authorization-uid", "same-socket", "unsupported-protocol", "trailing", "oversized"} {
		t.Run(fault, func(t *testing.T) {
			path, value := deploymentFixture(t)
			engine := value["engine"].(map[string]any)
			gateways := value["gateways"].([]any)
			switch fault {
			case "remote-http":
				engine["endpoint"] = "http://192.0.2.1:18191"
			case "https-no-client":
				engine["endpoint"] = "https://engine.example.test"
			case "engine-credentials":
				engine["endpoint"] = "http://user:secret@127.0.0.1:18191"
			case "missing-store":
				delete(engine, "store_id")
			case "unknown":
				value["legacy_runsc_root"] = "/host"
			case "short-lease":
				value["lease_ttl"] = "30s"
			case "lease":
				value["lease_ttl"] = "0s"
			case "zero-cpu":
				value["plans"].(map[string]any)["sh"].(map[string]any)["cpu_millis"] = 0
			case "manifest":
				value["manifest_digest"] = "sha256:" + strings.Repeat("f", 64)
			case "gateway-uid":
				delete(gateways[0].(map[string]any), "peer_uid")
			case "authorization-uid":
				delete(value["authorization"].(map[string]any), "peer_uid")
			case "same-socket":
				gateways[0].(map[string]any)["socket"] = "authorization.sock"
			case "unsupported-protocol":
				gateways[0].(map[string]any)["protocol"] = "unknown"
			}
			writeDeployment(t, path, value)
			if fault == "trailing" || fault == "oversized" {
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				suffix := "{}"
				if fault == "oversized" {
					suffix = strings.Repeat(" ", 1<<20)
				}
				if _, err := f.WriteString(suffix); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
			if _, err := LoadDeployment(path); err == nil {
				t.Fatalf("invalid %s deployment accepted", fault)
			}
		})
	}
}

func TestDeploymentEngineTLSAuthenticatesBothPeers(t *testing.T) {
	path, value := deploymentFixture(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-engine"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})
	for name, raw := range map[string][]byte{"engine-ca.pem": certPEM, "client.pem": certPEM, "client.key": keyPEM} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("engine request lacked authenticated client")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	engine := value["engine"].(map[string]any)
	engine["endpoint"], engine["ca_file"], engine["client_cert"], engine["client_key"] = server.URL, "engine-ca.pem", "client.pem", "client.key"
	writeDeployment(t, path, value)
	deployment, err := LoadDeployment(path)
	if err != nil {
		t.Fatal(err)
	}
	defer deployment.Engine.HTTPClient.CloseIdleConnections()
	response, err := deployment.Engine.HTTPClient.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("mutual TLS response: %d", response.StatusCode)
	}
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer untrusted.Close()
	if response, err := deployment.Engine.HTTPClient.Get(untrusted.URL); err == nil {
		response.Body.Close()
		t.Fatal("deployment trusted an unconfigured engine CA")
	}
}

func TestDeploymentSupportsLocalOnlyAndDirectOnlyWithoutGateways(t *testing.T) {
	for _, direct := range []bool{false, true} {
		path, value := deploymentFixture(t)
		delete(value, "gateways")
		delete(value, "authorization")
		if direct {
			value["connectivity"] = map[string]any{"revision": "direct-v1", "synthetic_range": "10.80.0.0/24", "policies": map[string]any{"sh": map[string]any{"direct": []any{map[string]any{"cidr": "198.51.100.8/32", "port": 9030, "proto": "tcp"}}}}}
		}
		writeDeployment(t, path, value)
		deployment, err := LoadDeployment(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(deployment.Gateways) != 0 || deployment.AuthorizationSocket != "" {
			t.Fatal("local deployment requires a gateway")
		}
		if direct && len(deployment.Connectivity.Policies["sh"].Direct) != 1 {
			t.Fatal("direct policy lost")
		}
	}
}
