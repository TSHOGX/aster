package producthttp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/producttest"
	"harness-platform/orchestrator/internal/testfixture"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
)

type rejectFileBackend struct {
	sandbox.VolumeReads
	sandbox.VolumeListings
	sandbox.VolumeFileMutations
	calls int
}

func (b *rejectFileBackend) StartProcess(context.Context, sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	b.calls++
	return nil, errors.New("lost start reply")
}
func (b *rejectFileBackend) ConnectProcess(context.Context, sandbox.ProcessReference, uint64) (sandbox.ProcessStream, error) {
	return nil, errors.New("offline")
}
func (b *rejectFileBackend) SendProcessInput(context.Context, sandbox.ProcessReference, []byte) error {
	return nil
}
func (b *rejectFileBackend) SignalProcess(context.Context, sandbox.ProcessReference, int32) error {
	return nil
}
func (b *rejectFileBackend) CloseProcessInput(context.Context, sandbox.ProcessReference) error {
	return nil
}
func (b *rejectFileBackend) WatchWorkspace(context.Context, sandbox.Reference, string, uint64) (sandbox.FileWatch, error) {
	b.calls++
	return nil, errors.New("offline")
}

func TestFileHTTPBoundaryAndUnknownOutcome(t *testing.T) {
	store, err := producttest.Open(ctx, filepath.Join(t.TempDir(), "files.db"))
	must(t, err)
	defer store.Close()
	scope := product.Scope{Actor: "alice", ProjectID: "files"}
	_, err = store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Files")
	must(t, err)
	lifecycle := &product.Lifecycle{VerifyRuntime: testfixture.VerifyRuntime, Store: store, Backend: &fakeLifecycleBackend{}, MaxProjects: 2}
	must(t, lifecycle.EnsureActive(ctx, scope, testProfile().Bindings()))
	for i := 0; i < 20; i++ {
		err = lifecycle.Reconcile(ctx, scope.ProjectID)
		if errors.Is(err, product.ErrNotFound) {
			break
		}
		must(t, err)
	}
	backend := &rejectFileBackend{}
	actor := "alice"
	server := (&Server{Store: store, Lifecycle: lifecycle, Files: &product.Files{Store: store, Lifecycle: lifecycle, Backend: backend, Bindings: testProfile().Bindings()}, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return actor, nil })}).Handler()
	request := func(method, path, body, origin string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://app"+path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		result := httptest.NewRecorder()
		server.ServeHTTP(result, req)
		return result
	}
	for _, body := range []string{`{"id":"bad","operation":"remove","path":"../outside"}`, `{"id":"bad","operation":"write","path":"file"}`, `{"id":"bad","operation":"mkdir","path":"ok","extra":true}`} {
		if got := request("POST", "/api/projects/files/files", body, "http://app"); got.Code != 400 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	body := `{"id":"mkdir","operation":"mkdir","path":"directory"}`
	if got := request("POST", "/api/projects/files/files", body, "http://foreign"); got.Code != 403 {
		t.Fatal(got.Code)
	}
	actor = "bob"
	if got := request("POST", "/api/projects/files/files", body, "http://app"); got.Code != 404 {
		t.Fatal(got.Code, got.Body.String())
	}
	actor = ""
	if got := request("GET", "/api/projects/files/files/operations/mkdir", "", "http://app"); got.Code != 401 {
		t.Fatal(got.Code)
	}
	if backend.calls != 0 {
		t.Fatal("unauthorized/invalid request dispatched")
	}
	actor = "alice"
	got := request("POST", "/api/projects/files/files", body, "http://app")
	if got.Code != 409 || !strings.Contains(got.Body.String(), `"state":"unknown"`) {
		t.Fatal(got.Code, got.Body.String())
	}
	got = request("POST", "/api/projects/files/files", body, "http://app")
	if got.Code != 409 || backend.calls != 1 {
		t.Fatal("lost operation was replayed", got.Code, backend.calls)
	}
	got = request("GET", "/api/projects/files/files/operations/mkdir", "", "http://app")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"unknown"`) {
		t.Fatal(got.Code, got.Body.String())
	}
	actor = "bob"
	if got = request("GET", "/api/projects/files/files/operations/mkdir", "", "http://app"); got.Code != 404 {
		t.Fatal(got.Code)
	}
}

func TestFileHTTPActionsIgnoreForgedHeaders(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "file-authority.db"))
	must(t, err)
	h := harnessForStore(t, store)
	owner, err := store.BootstrapAccount(ctx, "owner", "owner-password-before", identitySeed(t))
	must(t, err)
	h.setActor(owner.ID)
	h.terminal(t, "files", "shell")
	_, token, err := store.Authenticate(ctx, "owner", "owner-password-before")
	must(t, err)
	policies, err := store.Policies(ctx, token, "", 50)
	must(t, err)
	policy := policies.Policies[0]
	for _, action := range []product.ProductAction{product.ActionFilesUpload, product.ActionFilesExport, product.ActionFilesManage} {
		policy.Statements[action] = product.PolicyDeny
	}
	_, err = store.SavePolicy(ctx, token, policy.ID, product.PolicyChange{RequestID: "deny-file-actions", ExpectedRevision: policy.Revision, Statements: &policy.Statements})
	must(t, err)
	backend := &rejectFileBackend{}
	files := &product.Files{Store: store, Lifecycle: h.lifecycle, Backend: backend, Bindings: testProfile().Bindings()}
	defer files.Close()
	api := &Server{Store: store, Files: files, Lifecycle: h.lifecycle, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return owner.ID, nil })}
	defer api.Close()
	handler := api.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://app/api/projects/files/files"+path, strings.NewReader(body))
		req.Header.Set("Origin", "http://app")
		req.Header.Set("Idempotency-Key", "write")
		req.Header.Set("X-Content-SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte(body))))
		req.Header.Set("X-File-Action", "files.edit")
		req.Header.Set("X-Product-Action", "files.read")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	for _, test := range []struct{ method, path, body string }{
		{"PUT", "/content?path=file.txt", "bytes"},
		{"GET", "/download?path=file.txt", ""},
		{"POST", "", `{"id":"mkdir","operation":"mkdir","path":"folder"}`},
	} {
		result := request(test.method, test.path, test.body)
		if result.Code != 403 {
			t.Fatal(test, result.Code, result.Body.String())
		}
	}
	if backend.calls != 0 {
		t.Fatal("denied operation reached substrate", backend.calls)
	}
	result := request("PUT", "/editor-content?path=file.txt", "bytes")
	if result.Code != 409 || !strings.Contains(result.Body.String(), `"state":"unknown"`) || backend.calls != 1 {
		t.Fatal("editor grant did not admit bounded mutation", result.Code, result.Body.String(), backend.calls)
	}
}
