package server

import (
	"harness-platform/orchestrator/internal/agents"

	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// newWorkspaceWriteServer builds a server with a provisioned workspace, matching
// newArtifactDownloadServer but exposing the config so upload/edit limits apply.
func newWorkspaceWriteServer(t *testing.T, sessionID string) (*Server, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testArtifactDownloadConfig(t, dir)
	cfg.MaxUploadBytes = 100 << 20
	cfg.MaxEditBytes = 1 << 20
	cfg.MaxUploadFiles = 2000
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		t.Fatalf("mkdir db state: %v", err)
	}
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	createServerTestSession(t, ctx, st, dir, sessionID, string(sessionstate.Created), time.Now().UTC(), nil)
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, cfg: cfg, store: st}
	root := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	files := fixtureFiles{root: root, store: st}
	srv.workspaceWrites = files
	srv.workspaceReads = files
	return srv, st, root

}

func multipartUpload(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for fieldName, content := range files {
		// Field name carries the relative path; the filename is deliberately a
		// throwaway to prove the handler ignores it for path purposes.
		part, err := mw.CreateFormFile(fieldName, "ignored.bin")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, mw.FormDataContentType()
}

func TestUploadArtifactsWritesNestedFiles(t *testing.T) {
	srv, _, workspace := newWorkspaceWriteServer(t, "sess_1")
	body, contentType := multipartUpload(t, map[string]string{
		"src/utils/a.ts": "export const a = 1;",
		"README.md":      "# hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(workspace, "src", "utils", "a.ts"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(got) != "export const a = 1;" {
		t.Fatalf("unexpected content %q", got)
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatalf("stat README: %v", err)
	}
	// Uploaded files must be group-readable so the sandbox agent can read them.
	info, err := os.Stat(filepath.Join(workspace, "README.md"))
	if err != nil {
		t.Fatalf("stat README mode: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected mode 0640, got %o", info.Mode().Perm())
	}
}

func TestUploadArtifactsRejectsTraversal(t *testing.T) {
	srv, _, workspace := newWorkspaceWriteServer(t, "sess_1")
	body, contentType := multipartUpload(t, map[string]string{
		"../escape.txt": "pwned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(workspace), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal file should not exist: %v", err)
	}
}

func TestUploadArtifactsRejectsBackslash(t *testing.T) {
	srv, _, _ := newWorkspaceWriteServer(t, "sess_1")
	body, contentType := multipartUpload(t, map[string]string{
		`bad\path.txt`: "x",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUploadArtifactsRejectsSymlinkParent(t *testing.T) {
	srv, _, workspace := newWorkspaceWriteServer(t, "sess_1")
	dir := t.TempDir()
	outsideDir := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(workspace, "linked")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	body, contentType := multipartUpload(t, map[string]string{
		"linked/evil.txt": "pwned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink-escaped file should not exist: %v", err)
	}
}

func TestUploadArtifactsNonOwnerReturns404(t *testing.T) {
	srv, st, _ := newWorkspaceWriteServer(t, "sess_1")
	if _, err := st.DBForTest().ExecContext(context.Background(),
		`UPDATE sessions SET user_id = ? WHERE id = ?`, "someone-else", "sess_1"); err != nil {
		t.Fatalf("reassign owner: %v", err)
	}
	body, contentType := multipartUpload(t, map[string]string{"a.txt": "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUploadArtifactsTurnInProgressReturns409(t *testing.T) {
	srv, st, _ := newWorkspaceWriteServer(t, "sess_1")
	if _, err := st.EnqueueTurn(context.Background(), "sess_1", "hello", time.Now().UTC()); err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	body, contentType := multipartUpload(t, map[string]string{"a.txt": "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "turn_in_progress") {
		t.Fatalf("expected turn_in_progress error_class, got %s", rec.Body.String())
	}
}

func TestUploadArtifactsTooManyFilesReturns413(t *testing.T) {
	srv, _, _ := newWorkspaceWriteServer(t, "sess_1")
	srv.cfg.MaxUploadFiles = 1
	body, contentType := multipartUpload(t, map[string]string{
		"a.txt": "1",
		"b.txt": "2",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUploadArtifactsPerFileTooLargeReturns413(t *testing.T) {
	srv, _, _ := newWorkspaceWriteServer(t, "sess_1")
	srv.cfg.MaxUploadBytes = 8 // 8 bytes total budget; slack allows the framing
	body, contentType := multipartUpload(t, map[string]string{
		"big.txt": "this content is definitely larger than eight bytes",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.uploadArtifacts(rec, req, "sess_1")

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestWriteArtifactOverwritesFile(t *testing.T) {
	srv, _, workspace := newWorkspaceWriteServer(t, "sess_1")
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("old"), 0o640); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/artifacts/sess_1/note.txt", strings.NewReader("new content"))
	rec := httptest.NewRecorder()

	srv.writeArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(workspace, "note.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "new content" {
		t.Fatalf("unexpected content %q", got)
	}
}

func TestWriteArtifactStaleEditReturns409(t *testing.T) {
	srv, _, workspace := newWorkspaceWriteServer(t, "sess_1")
	target := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(target, []byte("current"), 0o640); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	// The client claims it loaded the file an hour ago; the file on disk is
	// newer, so the edit is stale.
	stale := time.Now().UTC().Add(-time.Hour)
	req := httptest.NewRequest(http.MethodPut, "/artifacts/sess_1/note.txt", strings.NewReader("clobber"))
	req.Header.Set("If-Unmodified-Since", stale.Format(http.TimeFormat))
	rec := httptest.NewRecorder()

	srv.writeArtifact(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stale_edit") {
		t.Fatalf("expected stale_edit error_class, got %s", rec.Body.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "current" {
		t.Fatalf("file should be unchanged, got %q", got)
	}
}

func TestWriteArtifactFreshEditSucceeds(t *testing.T) {
	srv, _, workspace := newWorkspaceWriteServer(t, "sess_1")
	target := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(target, []byte("current"), 0o640); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Client loaded at (or after) the file's mtime, so the edit is not stale.
	req := httptest.NewRequest(http.MethodPut, "/artifacts/sess_1/note.txt", strings.NewReader("updated"))
	req.Header.Set("If-Unmodified-Since", info.ModTime().UTC().Add(time.Second).Format(http.TimeFormat))
	rec := httptest.NewRecorder()

	srv.writeArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestWriteArtifactTooLargeReturns413(t *testing.T) {
	srv, _, _ := newWorkspaceWriteServer(t, "sess_1")
	srv.cfg.MaxEditBytes = 4
	req := httptest.NewRequest(http.MethodPut, "/artifacts/sess_1/note.txt", strings.NewReader("way too big"))
	rec := httptest.NewRecorder()

	srv.writeArtifact(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestWriteArtifactTurnInProgressReturns409(t *testing.T) {
	srv, st, _ := newWorkspaceWriteServer(t, "sess_1")
	if _, err := st.EnqueueTurn(context.Background(), "sess_1", "hello", time.Now().UTC()); err != nil {
		t.Fatalf("enqueue turn: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/artifacts/sess_1/note.txt", strings.NewReader("x"))
	rec := httptest.NewRecorder()

	srv.writeArtifact(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
}

type heldUploadReader struct {
	entered, release chan struct{}
	reader           *strings.Reader
	once             sync.Once
}

func (r *heldUploadReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.reader.Read(buffer)
}
