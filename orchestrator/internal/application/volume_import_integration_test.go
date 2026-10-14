//go:build khoraintegration

package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/producthttp"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/volumeimport"
)

func TestStudioImportOnRealKhora(t *testing.T) { qualifyStudioImport(t, content.AuthoredProfile) }
func TestSharedAssetsImportOnRealKhora(t *testing.T) {
	qualifyStudioImport(t, content.SharedAssetsProfile)
}
func qualifyStudioImport(t *testing.T, profile string) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	studioBinary := os.Getenv("STUDIO_TEST_BINARY")
	if endpoint == "" || studioBinary == "" {
		t.Fatal("isolated engine endpoint and real Studio executable required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	engineAPI, err := sdk.New(endpoint)
	must(err)
	for n := 0; n < 100; n++ {
		if engineAPI.Ready(ctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(ctx, khora.Config{Endpoint: endpoint})
	must(err)
	identity, err := backend.Identity(ctx)
	must(err)
	config, configPath := configurationFixture(t)
	config.Engine.StoreID = identity.StoreID
	raw, err := json.Marshal(config)
	must(err)
	must(os.WriteFile(configPath, raw, 0600))
	cfg, err := LoadConfig(configPath)
	must(err)
	_, err = AccountOperation(ctx, cfg, "bootstrap", "import-admin", "permanent-admin-password")
	must(err)
	store, err := product.Open(ctx, cfg.DatabasePath())
	must(err)
	defer store.Close()
	_, token, err := store.Authenticate(ctx, "import-admin", "permanent-admin-password")
	must(err)
	const issuer = "https://studio-test.example"
	secret := strings.Repeat("s", 48)
	t.Setenv("STUDIO_TEST_SECRET", secret)
	auth := producthttp.NewAuthentication(store, producthttp.Config{Identity: &identityconfig.Config{Issuer: issuer, Listen: "127.0.0.1:0", Clients: []identityconfig.Client{{ID: "studio", Callback: issuer + "/volumes-studio/auth/callback", SecretEnvs: []string{"STUDIO_TEST_SECRET"}}}}})
	central := httptest.NewServer(auth.IdentityServiceHandler())
	defer central.Close()
	public := auth.Handler(http.NotFoundHandler())
	freeAddr := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		must(err)
		addr := l.Addr().String()
		l.Close()
		return addr
	}
	gatewayAddr, distributionAddr := freeAddr(), freeAddr()
	root := t.TempDir()
	registrations := filepath.Join(root, "distribution.json")
	must(os.WriteFile(registrations, []byte(`{"clients":[{"id":"consumer","secret_envs":["STUDIO_TEST_SECRET"],"grant_revision":1,"libraries":["*"]}]}`), 0600))
	logfile, err := os.Create(filepath.Join(root, "studio.log"))
	must(err)
	defer logfile.Close()
	process := exec.CommandContext(ctx, studioBinary, "-listen", gatewayAddr, "-state-directory", filepath.Join(root, "studio"), "-public-origin", issuer, "-identity-endpoint", central.URL, "-identity-secret-env", "STUDIO_TEST_SECRET", "-publisher-id", "publisher", "-distribution-config", registrations, "-distribution-listen", distributionAddr)
	process.Env = os.Environ()
	process.Stdout = logfile
	process.Stderr = logfile
	must(process.Start())
	stopped := false
	defer func() {
		if !stopped {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	}()
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, body io.Reader, cookie, csrf string) (*http.Response, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, "http://"+gatewayAddr+path, body)
		must(err)
		r.Host = "studio-test.example"
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Origin", issuer)
		}
		r.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(r)
		must(err)
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		must(err)
		return resp, data
	}
	for n := 0; n < 100; n++ {
		c, err := net.DialTimeout("tcp", gatewayAddr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	login, _ := call("GET", "/volumes-studio/auth/login", nil, "", "")
	if login.StatusCode != 303 {
		t.Fatal(login.StatusCode)
	}
	location, err := url.Parse(login.Header.Get("Location"))
	must(err)
	r := httptest.NewRequest("GET", location.String(), nil)
	r.AddCookie(&http.Cookie{Name: "project_login", Value: token})
	w := httptest.NewRecorder()
	public.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	callback, err := url.Parse(w.Header().Get("Location"))
	must(err)
	callbackResponse, _ := call("GET", callback.RequestURI(), nil, login.Cookies()[0].String(), "")
	var cookie string
	for _, c := range callbackResponse.Cookies() {
		if c.Name == "__Secure-volumes_studio_session" {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("real Studio login failed")
	}
	_, sessionRaw := call("GET", "/volumes-studio/api/v1/session", nil, cookie, "")
	var session struct {
		CSRF string `json:"csrf"`
	}
	must(json.Unmarshal(sessionRaw, &session))
	jsonCall := func(method, path string, body any, status int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		must(err)
		response, data := call(method, "/volumes-studio/api/v1"+path, bytes.NewReader(raw), cookie, session.CSRF)
		if response.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
		}
		return data
	}
	var library struct {
		ID string `json:"id"`
	}
	must(json.Unmarshal(jsonCall("POST", "/libraries", map[string]any{"request_id": "library", "name": "Engine qualification", "profile": profile}, 201), &library))
	base := "/libraries/" + library.ID
	fileBytes := int64(32 << 20)
	fileCount := 1
	if profile == content.SharedAssetsProfile {
		fileBytes = 8 << 20
	}
	if os.Getenv("STUDIO_TEST_MAX_SIZE") == "1" {
		fileBytes = 256 << 20
		fileCount = 4
	}
	chunk := make([]byte, 8<<20)
	var revision int64 = 1
	if profile == content.SharedAssetsProfile {
		jsonCall("POST", base+"/draft/mutations", map[string]any{"request_id": "agents", "expected_revision": revision, "action": "text", "path": "AGENTS.md", "text": "Shared import qualification"}, 200)
		revision++
		jsonCall("POST", base+"/draft/mutations", map[string]any{"request_id": "skills", "expected_revision": revision, "action": "mkdir", "path": "skills"}, 200)
		revision++
	}
	started := time.Now()
	for file := 0; file < fileCount; file++ {
		for i := range chunk {
			chunk[i] = byte(file + 17)
		}
		digest := sha256.New()
		for n := int64(0); n < fileBytes; n += int64(len(chunk)) {
			_, _ = digest.Write(chunk)
		}
		whole := "sha256:" + hex.EncodeToString(digest.Sum(nil))
		sum := sha256.Sum256(chunk)
		partDigest := "sha256:" + hex.EncodeToString(sum[:])
		name := fmt.Sprintf("data-%d.bin", file)
		var upload struct {
			ID string `json:"id"`
		}
		must(json.Unmarshal(jsonCall("POST", base+"/uploads", map[string]any{"request_id": fmt.Sprintf("upload-%d", file), "expected_revision": revision, "path": name, "size": strconv.FormatInt(fileBytes, 10), "digest": whole}, 201), &upload))
		for offset := int64(0); offset < fileBytes; offset += int64(len(chunk)) {
			r, err := http.NewRequestWithContext(ctx, "PUT", "http://"+gatewayAddr+"/volumes-studio/api/v1"+base+"/uploads/"+upload.ID+"/chunks/"+strconv.FormatInt(offset, 10), bytes.NewReader(chunk))
			must(err)
			r.Host = "studio-test.example"
			r.Header.Set("Cookie", cookie)
			r.Header.Set("Origin", issuer)
			r.Header.Set("X-CSRF-Token", session.CSRF)
			r.Header.Set("X-Chunk-Digest", partDigest)
			response, err := client.Do(r)
			must(err)
			data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			must(err)
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode, string(data))
			}
		}
		jsonCall("POST", base+"/uploads/"+upload.ID+"/complete", map[string]any{"request_id": fmt.Sprintf("complete-%d", file), "expected_revision": revision}, 200)
		revision++
	}
	var version struct {
		ID     string `json:"id"`
		Digest string `json:"descriptor_digest"`
		State  string `json:"state"`
	}
	must(json.Unmarshal(jsonCall("POST", base+"/versions", map[string]any{"request_id": "publish", "expected_revision": revision, "label": "qualified"}, 202), &version))
	for n := 0; n < 180; n++ {
		must(json.Unmarshal(jsonCall("GET", base+"/versions/"+version.ID, nil, 200), &version))
		if version.State == "published" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if version.State != "published" {
		t.Fatal("publication did not settle", version.State)
	}
	source := volumeimport.Source{ID: "studio", PublisherID: "publisher", Endpoint: "http://" + distributionAddr, ClientID: "consumer", SecretEnv: "STUDIO_TEST_SECRET", Libraries: []string{library.ID}}
	connector, err := volumeimport.NewConnector(source)
	must(err)
	descriptor, raw, err := connector.Manifest(ctx, version.ID, version.Digest)
	must(err)
	input := product.VolumeImportInput{RequestID: "import", SourceID: source.ID, VersionID: version.ID, ExpectedManifestDigest: version.Digest, ConsumerProfile: profile}
	imported, err := store.AdmitVolumeImport(ctx, token, input, raw, identity.StoreID, "central/author", 4<<30, 10<<30)
	must(err)
	cfg.VolumeImports = &VolumeImportConfig{Sources: []volumeimport.Source{source}, TransferBytes: 4 << 30, EngineBytes: 10 << 30}
	app := &Application{Store: store, Backend: backend, Config: cfg}
	cache, err := volumeimport.Admit(filepath.Join(cfg.StateDirectory, "volume-transfers", imported.ID), volumeimport.Identity{ID: imported.ID, SourceID: source.ID, PublisherID: "publisher", VersionID: version.ID, DescriptorDigest: version.Digest, Profile: profile}, raw)
	must(err)
	must(connector.Acquire(ctx, cache))
	must(cache.Close())
	must(process.Process.Signal(os.Interrupt))
	must(process.Wait())
	stopped = true
	// Both engine materialization and final registry admission now use local bytes.
	must(app.performVolumeImport(ctx, imported))
	ready, err := store.VolumeImport(ctx, imported.ID)
	must(err)
	if ready.State != "ready" || ready.TransferReserved != 0 {
		t.Fatal(ready)
	}
	volume, found, err := store.FindPublishedContent(ctx, identity.StoreID, ready.PublicationPurpose(), descriptor.TreeDigest)
	must(err)
	if !found {
		t.Fatal("ready publication missing")
	}
	ref := sandbox.VolumeReference{StoreID: identity.StoreID, VolumeID: volume}
	must(content.VerifyVolume(ctx, backend, ref, descriptor.TreeDigest, profile))
	copyA, err := backend.SnapshotVolume(ctx, ref, "qualified-owned-a-"+profile, ready.Capacity)
	must(err)
	copyB, err := backend.SnapshotVolume(ctx, ref, "qualified-owned-b-"+profile, ready.Capacity)
	must(err)
	must(backend.WriteVolume(ctx, copyA.VolumeReference, "user-note.txt", []byte("user edits survive")))
	if _, err = backend.LstatVolume(ctx, copyB.VolumeReference, "user-note.txt"); !sandbox.Is(err, sandbox.NotFound) {
		t.Fatal("owned copies shared edits", err)
	}
	must(content.VerifyVolume(ctx, backend, ref, descriptor.TreeDigest, profile))
	retiring, err := store.RetireImportedVolume(ctx, token, imported.ID, "retire")
	must(err)
	if err = app.cleanupVolumeImport(ctx, retiring); !sandbox.Is(err, sandbox.Conflict) {
		t.Fatal("descendants did not block retirement", err)
	}
	usage, err := store.OperatorStorageUsage(ctx)
	must(err)
	if usage.Publications.Used < 2*ready.Capacity {
		t.Fatal("blocked cleanup released capacity")
	}
	must(backend.DeleteVolume(ctx, copyA.VolumeReference))
	must(backend.DeleteVolume(ctx, copyB.VolumeReference))
	must(app.cleanupVolumeImport(ctx, retiring))
	final, err := store.VolumeImport(ctx, imported.ID)
	must(err)
	if final.State != "retired" {
		t.Fatal(final.State)
	}
	if _, err = backend.InspectVolume(ctx, ref); !sandbox.Is(err, sandbox.NotFound) {
		t.Fatal("retired derivative remains", err)
	}
	t.Logf("real Studio -> independent consumer -> Khora: %d files x %d bytes; offline source acquisition, stream/readback, copy isolation and descendant cleanup in %s", fileCount, fileBytes, time.Since(started))
}
