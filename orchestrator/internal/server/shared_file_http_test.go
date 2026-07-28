package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
)

// newFileShareFixture is the artifact fixture plus a blob store, i.e. a
// deployment where public file sharing is configured.
func newFileShareFixture(t *testing.T) (*shareFixture, string) {
	t.Helper()
	f, workspace := newArtifactShareFixture(t)
	blobs, err := shareblob.New(filepath.Join(f.dir, "share-blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}
	f.srv.shareBlobs = blobs
	f.srv.cfg.MaxShareLinkTTL = 30 * 24 * time.Hour
	f.srv.cfg.MaxShareLinkBytes = 1 << 20
	f.srv.cfg.MaxShareLinkFiles = 5
	return f, workspace
}

func (f *shareFixture) mintFileLink(t *testing.T, body string) (string, apiShareLink) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_alice/share-links", strings.NewReader(body)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint link: want 201 got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Link  apiShareLink `json:"link"`
		Token string       `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return payload.Token, payload.Link
}

func sharedFileGet(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.sharedFile(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The core snapshot property: the bytes a published URL returns do not change
// when the agent rewrites the file, and keep working after the session is
// destroyed and its workspace is gone.
func TestSharedFileServesFrozenSnapshot(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	target := filepath.Join(workspace, "report.html")
	if err := os.WriteFile(target, []byte("<html>original</html>"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	token, link := f.mintFileLink(t, `{"visibility":"full","files":["report.html"]}`)
	if link.FileCount != 1 {
		t.Fatalf("expected 1 whitelisted file, got %d", link.FileCount)
	}

	rec := sharedFileGet(t, f.srv, "/shared/"+token+"/files/report.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "original") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}

	// The agent overwrites the live file.
	if err := os.WriteFile(target, []byte("<html>rewritten later</html>"), 0o644); err != nil {
		t.Fatalf("rewrite artifact: %v", err)
	}
	rec = sharedFileGet(t, f.srv, "/shared/"+token+"/files/report.html")
	if !strings.Contains(rec.Body.String(), "original") || strings.Contains(rec.Body.String(), "rewritten") {
		t.Fatalf("the link must keep serving the snapshot: %q", rec.Body.String())
	}

	// The workspace disappears entirely (session destroyed, volume reclaimed).
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	rec = sharedFileGet(t, f.srv, "/shared/"+token+"/files/report.html")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "original") {
		t.Fatalf("the link must survive workspace loss: %d %q", rec.Code, rec.Body.String())
	}
}

// Only whitelisted paths exist on this surface. A workspace file that was not
// selected is not reachable, and neither is a traversal attempt.
func TestSharedFileServesOnlyTheWhitelist(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	for name, content := range map[string]string{
		"shared.txt":  "public",
		"private.txt": "not selected for sharing",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	token, _ := f.mintFileLink(t, `{"visibility":"full","files":["shared.txt"]}`)

	if rec := sharedFileGet(t, f.srv, "/shared/"+token+"/files/shared.txt"); rec.Code != http.StatusOK {
		t.Fatalf("whitelisted file: want 200 got %d", rec.Code)
	}
	for _, path := range []string{
		"/shared/" + token + "/files/private.txt",
		"/shared/" + token + "/files/../private.txt",
		"/shared/" + token + "/files/..%2fprivate.txt",
		"/shared/" + token + "/files/",
		"/shared/" + token + "/private.txt",
		"/shared/bogus-token/files/shared.txt",
	} {
		if rec := sharedFileGet(t, f.srv, path); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404 got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

// A revoked or expired link's files must stop being served with the link.
func TestSharedFileFollowsLinkLifecycle(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "chart.html"), []byte("<html>c</html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	token, link := f.mintFileLink(t, `{"visibility":"full","files":["chart.html"]}`)
	if rec := sharedFileGet(t, f.srv, "/shared/"+token+"/files/chart.html"); rec.Code != http.StatusOK {
		t.Fatalf("before revoke: want 200 got %d", rec.Code)
	}

	rec := httptest.NewRecorder()
	f.srv.deleteShareLink(rec, asUser(httptest.NewRequest(http.MethodDelete, "/x", nil), "alice"), "sess_alice", link.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := sharedFileGet(t, f.srv, "/shared/"+token+"/files/chart.html"); got.Code != http.StatusNotFound {
		t.Fatalf("after revoke: want 404 got %d", got.Code)
	}
}

// HTML and PDF must be framable same-origin so the public page can preview them;
// everything else stays inert. This is the pairing that broke previews before
// (X-Frame-Options collision), so it is pinned.
func TestSharedFileContentHeaders(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	for _, name := range []string{"page.html", "doc.pdf", "data.csv"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	token, _ := f.mintFileLink(t, `{"visibility":"full","files":["page.html","doc.pdf","data.csv"]}`)

	cases := map[string]struct {
		frameOptions string
		cspContains  string
	}{
		"page.html": {frameOptions: "SAMEORIGIN", cspContains: "connect-src 'none'"},
		"doc.pdf":   {frameOptions: "SAMEORIGIN", cspContains: "frame-ancestors 'self'"},
		"data.csv":  {frameOptions: "DENY", cspContains: "sandbox"},
	}
	for name, want := range cases {
		rec := sharedFileGet(t, f.srv, "/shared/"+token+"/files/"+name)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200 got %d", name, rec.Code)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != want.frameOptions {
			t.Fatalf("%s: X-Frame-Options=%q want %q", name, got, want.frameOptions)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want.cspContains) {
			t.Fatalf("%s: CSP %q must contain %q", name, csp, want.cspContains)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: expected nosniff, got %q", name, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Fatalf("%s: expected no-referrer, got %q", name, got)
		}
	}
}

func TestShareLinkFileCapsAreEnforced(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	f.srv.cfg.MaxShareLinkFiles = 2
	f.srv.cfg.MaxShareLinkBytes = 64
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(strings.Repeat("x", 50)), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Too many files.
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/x",
		strings.NewReader(`{"visibility":"full","files":["a.txt","b.txt","c.txt"]}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("file count over cap: want 400 got %d (%s)", rec.Code, rec.Body.String())
	}

	// Within the count cap but over the byte cap (2 x 50 > 64).
	rec = httptest.NewRecorder()
	req = asUser(httptest.NewRequest(http.MethodPost, "/x",
		strings.NewReader(`{"visibility":"full","files":["a.txt","b.txt"]}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("bytes over cap: want 413 got %d (%s)", rec.Code, rec.Body.String())
	}

	// No link row was created by either rejection.
	links, err := f.store.ListShareLinks(context.Background(), "sess_alice")
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("a rejected create must not leave a link row, got %d", len(links))
	}
}

// A file that does not exist in the workspace must fail the create outright
// rather than producing a link with a broken entry.
func TestShareLinkRejectsMissingFile(t *testing.T) {
	f, _ := newFileShareFixture(t)
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/x",
		strings.NewReader(`{"visibility":"full","files":["nope.txt"]}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing file: want 404 got %d (%s)", rec.Code, rec.Body.String())
	}
	links, _ := f.store.ListShareLinks(context.Background(), "sess_alice")
	if len(links) != 0 {
		t.Fatalf("expected no link row, got %d", len(links))
	}
}

// With no blob store configured, a file request is refused rather than silently
// falling back to the live workspace.
func TestFileSharingRefusedWhenBlobStoreAbsent(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.srv.shareBlobs = nil

	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/x",
		strings.NewReader(`{"visibility":"full","files":["a.txt"]}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d (%s)", rec.Code, rec.Body.String())
	}
	// A text-only link still works.
	rec = httptest.NewRecorder()
	req = asUser(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"visibility":"full"}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusCreated {
		t.Fatalf("text-only link: want 201 got %d (%s)", rec.Code, rec.Body.String())
	}
}

// The anonymous artifact listing must report the whitelist, with sizes, and
// never the blob digest (an internal storage key).
func TestSharedArtifactsListsWhitelistWithoutDigests(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "chart.html"), []byte("<html>abc</html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.store.UpsertArtifact(context.Background(), store.Artifact{
		SessionID: "sess_alice", Path: "internal-dump.csv", Size: 99, ModTime: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	token, _ := f.mintFileLink(t, `{"visibility":"full","files":["chart.html"]}`)

	rec := sharedGet(t, f.srv, "/api/shared/"+token+"/artifacts")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "chart.html") {
		t.Fatalf("expected the whitelisted file: %s", body)
	}
	if strings.Contains(body, "internal-dump.csv") {
		t.Fatalf("the live workspace must not leak into the listing: %s", body)
	}
	if strings.Contains(body, "blob") || strings.Contains(body, "sha256") {
		t.Fatalf("the blob digest is internal and must not be published: %s", body)
	}
	var payload struct {
		Artifacts []apiSharedLinkFile `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Artifacts) != 1 || payload.Artifacts[0].Size != int64(len("<html>abc</html>")) {
		t.Fatalf("unexpected whitelist: %+v", payload.Artifacts)
	}
}

// GC must reclaim only blobs that lost their LAST reference, and must be driven
// by the sweep's transactional verdict rather than a separate reachability guess.
func TestShareBlobReclamationRespectsSharedReferences(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	ctx := context.Background()
	// Same bytes in two files: the blob store dedupes them to one digest, which is
	// exactly the case a naive per-link delete would get wrong.
	if err := os.WriteFile(filepath.Join(workspace, "shared.html"), []byte("<html>same</html>"), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "unique.html"), []byte("<html>only here</html>"), 0o644); err != nil {
		t.Fatalf("write unique: %v", err)
	}

	// A link that will expire, holding both files.
	expiringToken, expiring := f.mintFileLink(t, `{"visibility":"full","files":["shared.html","unique.html"]}`)
	// A link that stays live, holding the shared bytes only.
	liveToken, _ := f.mintFileLink(t, `{"visibility":"full","files":["shared.html"]}`)

	// Age the first link out.
	if _, err := f.store.DBForTest().ExecContext(ctx,
		`UPDATE share_links SET expires_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), expiring.ID); err != nil {
		t.Fatalf("age link: %v", err)
	}

	f.srv.sweepExpiredShareLinks(ctx, time.Now().UTC())

	// The expired link is gone.
	if rec := sharedFileGet(t, f.srv, "/shared/"+expiringToken+"/files/unique.html"); rec.Code != http.StatusNotFound {
		t.Fatalf("expired link file: want 404 got %d", rec.Code)
	}
	// The live link's file still serves: its blob must NOT have been reclaimed
	// just because an expired link also referenced it.
	rec := sharedFileGet(t, f.srv, "/shared/"+liveToken+"/files/shared.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("live link file after gc: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "same") {
		t.Fatalf("live link bytes were damaged: %q", rec.Body.String())
	}

	// The uniquely-referenced blob is off disk.
	remaining := 0
	if err := filepath.Walk(f.srv.shareBlobs.Root(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			remaining++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk blobs: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected exactly the still-referenced blob on disk, found %d", remaining)
	}
}

// A whitelist row whose bytes are missing renders as 404, not a 500.
func TestSharedFileMissingBlobIs404(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	token, link := f.mintFileLink(t, `{"visibility":"full","files":["a.txt"]}`)
	files, err := f.store.ListShareLinkFiles(context.Background(), link.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("list files: %v (%d)", err, len(files))
	}
	if err := f.srv.shareBlobs.Remove(files[0].BlobSHA256); err != nil {
		t.Fatalf("remove blob: %v", err)
	}
	if rec := sharedFileGet(t, f.srv, "/shared/"+token+"/files/a.txt"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d (%s)", rec.Code, rec.Body.String())
	}
}

// The file subtree must be outside requireAuth, like /api/shared.
func TestSharedFileSubtreeBypassesAuth(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	token, _ := f.mintFileLink(t, `{"visibility":"full","files":["a.txt"]}`)

	routes := f.srv.Routes()
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/"+token+"/files/a.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous file fetch: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	// The authed artifact path still requires a cookie.
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts/sess_alice/a.txt", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/artifacts without a cookie: want 401 got %d", rec.Code)
	}
}

func TestSharedFileIsReadOnly(t *testing.T) {
	f, workspace := newFileShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	token, _ := f.mintFileLink(t, `{"visibility":"full","files":["a.txt"]}`)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		f.srv.sharedFile(rec, httptest.NewRequest(method, "/shared/"+token+"/files/a.txt", strings.NewReader("x")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: want 405 got %d", method, rec.Code)
		}
	}
}
