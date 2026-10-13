package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/identity"
)

func TestAuthoringHTTPJourneyAndActiveContentIsolation(t *testing.T) {
	store, err := authoring.Open(context.Background(), authoring.Config{Directory: filepath.Join(t.TempDir(), "studio"), PublisherID: "publisher", QuotaBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := NewAuthoringAPI(store)
	session := identity.Session{Issuer: "https://example.com", Subject: "admin"}
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://example.com"+apiPrefix+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.Serve(session, w, r)
		return w
	}
	w := call("POST", "libraries", map[string]any{"request_id": "create", "name": "Library", "description": "Test"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var l authoring.Library
	if err = json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	root := "libraries/" + l.ID
	html := `<html><script>document.body.innerHTML='active'</script></html>`
	w = call("POST", root+"/draft/mutations", map[string]any{"request_id": "save", "expected_revision": 1, "action": "text", "path": "active.html", "text": html})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "https://example.com"+apiPrefix+root+"/draft/content?path=active.html", nil)
	r.Header.Set("Range", "bytes=0-5")
	w = httptest.NewRecorder()
	api.Serve(session, w, r)
	if w.Code != 206 || w.Body.String() != html[:6] || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("active content boundary", w.Code, w.Header())
	}
	r.Header.Set("Range", "bytes=0-1,3-4")
	w = httptest.NewRecorder()
	api.Serve(session, w, r)
	if w.Code != 416 {
		t.Fatal("unbounded multipart range accepted")
	}
	data := []byte{0, 1, 2, 255}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	w = call("POST", root+"/uploads", map[string]any{"request_id": "upload", "expected_revision": 2, "path": "binary", "size": "4", "digest": digest})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var u authoring.Upload
	if err = json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", root+"/versions", map[string]any{"request_id": "publish", "expected_revision": 2, "label": "v1"}); w.Code != 409 {
		t.Fatal("upload did not block publish")
	}
	r = httptest.NewRequest("PUT", "https://example.com"+apiPrefix+root+"/uploads/"+u.ID+"/chunks/0", bytes.NewReader(data))
	r.Header.Set("X-Chunk-Digest", digest)
	w = httptest.NewRecorder()
	api.Serve(session, w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", root+"/uploads/"+u.ID+"/complete", map[string]any{"request_id": "complete", "expected_revision": 2}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", root+"/versions", map[string]any{"request_id": "publish", "expected_revision": 3, "label": "v1"})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v authoring.Version
	if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ValidateNext(context.Background(), func(context.Context, authoring.Actor) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w = call("GET", root+"/versions/"+v.ID+"/content?path="+url.QueryEscape("binary"), nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("published bytes", w.Code)
	}
	w = call("GET", "libraries/other/versions/"+v.ID, nil)
	if w.Code != 404 {
		t.Fatal("version escaped library scope", w.Code)
	}
	w = call("POST", root+"/draft/mutations", map[string]any{"request_id": "invalid", "expected_revision": 3, "action": "text", "path": "../escape", "text": "x"})
	if w.Code != 422 {
		t.Fatal("path validation", w.Code)
	}
}
