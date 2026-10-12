package volumeimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/volumeimport/manifest"
)

func TestConnectorResumeLostAdmissionAndOfflineAcquisition(t *testing.T) {
	data := []byte(strings.Repeat("x", int(MaxChunkBytes)+23))
	digest := hash(data)
	d, err := manifest.New("publisher", "library", "version", []manifest.Entry{{Kind: "file", Path: "data", Size: strconv.Itoa(len(data)), Digest: digest}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := d.Canonical()
	md, _ := d.Digest()
	var admissionLoss atomic.Bool
	admissionLoss.Store(true)
	var transferLoss atomic.Bool
	transferLoss.Store(true)
	var requests atomic.Int32
	var firstKey string
	secret := strings.Repeat("secret", 8)
	t.Setenv("SOURCE_TEST_SECRET", secret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "consumer" || pass != secret {
			t.Error("missing service identity")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/distribution/v1/versions/version/manifest":
			w.Header().Set("ETag", `"`+md+`"`)
			_, _ = w.Write(raw)
		case "/distribution/v1/exports":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if firstKey == "" {
				firstKey = body["request_id"]
			} else if firstKey != body["request_id"] {
				t.Error("lost admission did not replay original receipt")
			}
			if admissionLoss.Swap(false) {
				http.Error(w, "lost reply", 503)
				return
			}
			_ = json.NewEncoder(w).Encode(Export{ID: "export", VersionID: "version", DescriptorDigest: md, State: "open", ExpiresAt: time.Now().Add(15 * time.Minute).Unix()})
		case "/distribution/v1/exports/export":
			w.WriteHeader(204)
		case "/distribution/v1/exports/export/blobs/" + digest:
			requests.Add(1)
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				t.Error(err)
			}
			if start == MaxChunkBytes && transferLoss.Swap(false) {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("ETag", `"`+digest+`"`)
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.WriteHeader(206)
			_, _ = w.Write(data[start : end+1])
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	source := Source{ID: "source", PublisherID: "publisher", Endpoint: server.URL, ClientID: "consumer", SecretEnv: "SOURCE_TEST_SECRET", Libraries: []string{"library"}}
	connector, err := NewConnector(source)
	if err != nil {
		t.Fatal(err)
	}
	_, downloaded, err := connector.Manifest(context.Background(), "version", md)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "cache")
	cache, err := Admit(root, Identity{ID: "import", SourceID: "source", PublisherID: "publisher", VersionID: "version", DescriptorDigest: md, Profile: manifest.Profile}, downloaded)
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.Acquire(context.Background(), cache); err == nil {
		t.Fatal("lost export response hidden")
	}
	cache.Close()
	cache, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.Acquire(context.Background(), cache); err == nil {
		t.Fatal("lost range hidden")
	}
	if got := cache.Status().Blobs[0].Acquired; got != MaxChunkBytes {
		t.Fatal("durable checkpoint missing", got)
	}
	cache.Close()
	cache, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if err = connector.Acquire(context.Background(), cache); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || cache.Status().State != SourceAcquired {
		t.Fatal("acquisition restarted verified bytes", requests.Load(), cache.Status().State)
	}
	server.Close()
	if err = connector.Acquire(context.Background(), cache); err != nil {
		t.Fatal("ready source still needs Studio", err)
	}
}
func TestSourceRefusesRedirectCredentialPropagationAndDescriptorMismatch(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	t.Setenv("SOURCE_TEST_SECRET", strings.Repeat("s", 32))
	source := Source{ID: "source", PublisherID: "publisher", Endpoint: server.URL, ClientID: "consumer", SecretEnv: "SOURCE_TEST_SECRET", Libraries: []string{"library"}}
	connector, err := NewConnector(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = connector.Manifest(context.Background(), "version", hash(nil)); err == nil {
		t.Fatal("redirect accepted")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("credential sent to redirect target")
	}
	for _, endpoint := range []string{"http://remote.example", "https://user:pass@example.com", "https://example.com/path", "https://example.com?secret=value"} {
		source.Endpoint = endpoint
		if _, err := NewConnector(source); err == nil {
			t.Fatal("unsafe source accepted", endpoint)
		}
	}
}
