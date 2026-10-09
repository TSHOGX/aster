package producthttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

type readHTTPProcess struct {
	reader *io.PipeReader
	input  *io.PipeWriter
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *readHTTPProcess) Next() (sandbox.ProcessFrame, error) {
	data := make([]byte, 4096)
	n, err := p.reader.Read(data)
	return sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: data[:n]}, err
}
func (p *readHTTPProcess) Close() error { return p.reader.Close() }

type readerHTTPBackend struct {
	rejectFileBackend
	root      string
	mu        sync.Mutex
	processes map[string]*readHTTPProcess
	starts    int
}

func (b *readerHTTPBackend) StartProcess(_ context.Context, r sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	if len(r.Arguments) != 1 || r.Arguments[0] != "--reader" {
		return nil, errors.New("expected read-only service")
	}
	in, input := io.Pipe()
	reader, out := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	p := &readHTTPProcess{reader: reader, input: input, cancel: cancel, done: make(chan struct{})}
	b.mu.Lock()
	b.starts++
	b.processes[r.Handle] = p
	b.mu.Unlock()
	go func() {
		defer close(p.done)
		defer out.Close()
		_ = projectfiles.ServeReader(ctx, b.root, in, out, time.Minute)
	}()
	return p, nil
}
func (b *readerHTTPBackend) SendProcessInput(ctx context.Context, r sandbox.ProcessReference, data []byte) error {
	b.mu.Lock()
	p := b.processes[r.Handle]
	b.mu.Unlock()
	if p == nil {
		return io.ErrClosedPipe
	}
	stop := context.AfterFunc(ctx, func() { p.input.CloseWithError(ctx.Err()) })
	defer stop()
	_, err := p.input.Write(data)
	return err
}
func (b *readerHTTPBackend) SignalProcess(_ context.Context, r sandbox.ProcessReference, _ int32) error {
	b.mu.Lock()
	p := b.processes[r.Handle]
	b.mu.Unlock()
	if p != nil {
		p.cancel()
		p.reader.Close()
		p.input.Close()
		<-p.done
	}
	return &sandbox.Error{Class: sandbox.NotFound}
}
func TestFileReaderHTTPValidatorsRangesAndNoReadCommands(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "files"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Files")
	must(t, err)
	must(t, h.lifecycle.EnsureActive(ctx, scope, testProfile()))
	h.converge(t, "files")
	backend := &readerHTTPBackend{root: t.TempDir(), processes: map[string]*readHTTPProcess{}}
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("abcdefghij"), 0600))
	files := &product.Files{Store: h.store, Lifecycle: h.lifecycle, Backend: backend, Profile: testProfile()}
	defer files.Close()
	server := &Server{Store: h.store, Files: files, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return h.currentActor(), nil })}
	defer server.Close()
	handler := server.Handler()
	request := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "http://fixture/api/projects/files/files"+path, nil)
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	initial := request("/content?path=file", nil)
	if initial.Code != 200 || initial.Body.String() != "abcdefghij" {
		t.Fatal(initial.Code, initial.Body.String())
	}
	etag := initial.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing strong validator")
	}
	unchanged := request("/content?path=file", map[string]string{"If-None-Match": etag})
	if unchanged.Code != 304 || unchanged.Body.Len() != 0 {
		t.Fatal(unchanged.Code)
	}
	partial := request("/content?path=file", map[string]string{"Range": "bytes=2-4", "If-Range": etag})
	if partial.Code != 206 || partial.Body.String() != "cde" || partial.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatal(partial.Code, partial.Body.String(), partial.Header())
	}
	invalid := request("/content?path=file", map[string]string{"Range": "bytes=100-200"})
	if invalid.Code != 416 {
		t.Fatal(invalid.Code)
	}
	before, err := os.Stat(filepath.Join(backend.root, "file"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("ABCDEFGHIJ"), 0600))
	must(t, os.Chtimes(filepath.Join(backend.root, "file"), before.ModTime(), before.ModTime()))
	changed := request("/content?path=file", map[string]string{"If-None-Match": etag})
	if changed.Code != 200 || changed.Body.String() != "ABCDEFGHIJ" || changed.Header().Get("ETag") == etag {
		t.Fatal("same metadata hid changed bytes", changed.Code)
	}
	mismatch := request("/content?path=file", map[string]string{"If-Range": etag, "Range": "bytes=2-4"})
	if mismatch.Code != 200 || mismatch.Body.String() != "ABCDEFGHIJ" {
		t.Fatal(mismatch.Code, mismatch.Body.String())
	}
	for n := 0; n < 5; n++ {
		if r := request("?path=&limit=1", nil); r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	h.setActor("bob")
	denied := request("/content?path=file", map[string]string{"If-None-Match": changed.Header().Get("ETag")})
	if denied.Code != 404 {
		t.Fatal("validator bypassed authority", denied.Code)
	}
	h.setActor("alice")
	for _, path := range []string{"../secret", "/etc/passwd"} {
		r := request("/content?path="+url.QueryEscape(path), nil)
		if r.Code != 400 {
			t.Fatal(path, r.Code)
		}
	}
	must(t, os.Symlink("/etc/passwd", filepath.Join(backend.root, "escape")))
	if r := request("/content?path=escape", nil); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	backend.mu.Lock()
	starts := backend.starts
	backend.mu.Unlock()
	if starts != 1 {
		t.Fatal("one helper per request", starts)
	}
	blockers, err := h.store.Blockers(ctx, scope)
	must(t, err)
	for _, b := range blockers {
		if strings.Contains(b.Kind, "files") {
			t.Fatal("reads own durable activity", blockers)
		}
	}
}

func TestPreviewSnapshotHTTPImmutableRangesAndAuthority(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "snapshots"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Snapshots")
	must(t, err)
	must(t, h.lifecycle.EnsureActive(ctx, scope, testProfile()))
	h.converge(t, scope.ProjectID)
	backend := &readerHTTPBackend{root: t.TempDir(), processes: map[string]*readHTTPProcess{}}
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("abcdefghij"), 0600))
	files := &product.Files{Store: h.store, Lifecycle: h.lifecycle, Backend: backend, Profile: testProfile()}
	defer files.Close()
	server := &Server{Store: h.store, Files: files, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return h.currentActor(), nil })}
	defer server.Close()
	handler := server.Handler()
	request := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://fixture/api/projects/snapshots/files"+path, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	opened := request("GET", "/snapshots?path=file&max_bytes=10", nil)
	if opened.Code != 200 {
		t.Fatal(opened.Code, opened.Body.String())
	}
	var descriptor product.PreviewSnapshot
	must(t, json.Unmarshal(opened.Body.Bytes(), &descriptor))
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("ABCDEFGHIJ"), 0600))
	base := "/snapshots/" + descriptor.ID
	for n := 0; n < 10; n++ {
		rangeHeader := fmt.Sprintf("bytes=%d-%d", n, n)
		r := request("GET", base+"/content", map[string]string{"Range": rangeHeader})
		if r.Code != 206 || r.Body.String() != string(byte('a'+n)) {
			t.Fatal(r.Code, r.Body.String())
		}
		if r.Header().Get("ETag") != `"sha256:`+descriptor.Digest+`"` {
			t.Fatal("lost immutable identity")
		}
	}
	if r := request("GET", base+"/content", map[string]string{"Range": "bytes=0-1,3-4"}); r.Code != 400 {
		t.Fatal(r.Code)
	}
	if r := request("GET", "/snapshots?path=file&max_bytes=9", nil); r.Code != 413 || !strings.Contains(r.Body.String(), "source_too_large") {
		t.Fatal(r.Code, r.Body.String())
	}
	h.setActor("foreign")
	if r := request("GET", base+"/content", nil); r.Code != 404 {
		t.Fatal("foreign access", r.Code)
	}
	if r := request("DELETE", base, nil); r.Code != 404 {
		t.Fatal("foreign release", r.Code)
	}
	h.setActor("alice")
	if r := request("GET", base+"/content", nil); r.Body.String() != "abcdefghij" {
		t.Fatal("foreign release invalidated owner")
	}
	if r := request("DELETE", base, nil); r.Code != 204 {
		t.Fatal(r.Code)
	}
	if r := request("GET", base+"/content", nil); r.Code != 410 || !strings.Contains(r.Body.String(), "snapshot_expired") {
		t.Fatal(r.Code, r.Body.String())
	}
}
