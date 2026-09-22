package producthttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// ptyBackend is a scripted PTY substrate. The transport tests care about
// authorization, origin, ownership and framing, not about shell behavior; the
// real shell is the isolated-guest gate's subject.
type ptyBackend struct {
	mu      sync.Mutex
	streams map[string]*ptyStream
	input   []byte
	sizes   map[string][2]int
	signals []int32
	eof     int
}

type ptyStream struct {
	frames chan sandbox.ProcessFrame
	closed chan struct{}
	once   sync.Once
}

func (s *ptyStream) Next() (sandbox.ProcessFrame, error) {
	select {
	case f := <-s.frames:
		return f, nil
	case <-s.closed:
		return sandbox.ProcessFrame{}, errors.New("closed")
	}
}
func (s *ptyStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func newPTYBackend() *ptyBackend {
	return &ptyBackend{streams: map[string]*ptyStream{}, sizes: map[string][2]int{}}
}
func (b *ptyBackend) StartPTY(_ context.Context, r sandbox.StartProcessRequest, rows, cols int) (sandbox.ProcessStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &ptyStream{frames: make(chan sandbox.ProcessFrame, 64), closed: make(chan struct{})}
	b.streams[r.Handle] = s
	b.sizes[r.Handle] = [2]int{rows, cols}
	return s, nil
}
func (b *ptyBackend) stream(handle string) *ptyStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.streams[handle]
}
func (b *ptyBackend) StartProcess(context.Context, sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	return nil, errors.New("terminals always allocate a PTY")
}
func (b *ptyBackend) ConnectProcess(_ context.Context, r sandbox.ProcessReference, _ uint64) (sandbox.ProcessStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.streams[r.Handle]; s != nil {
		return s, nil
	}
	return nil, &sandbox.Error{Class: sandbox.NotFound, Operation: "connect", Cause: errors.New("absent")}
}
func (b *ptyBackend) SendPTYInput(_ context.Context, _ sandbox.ProcessReference, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.input = append(b.input, data...)
	return nil
}
func (b *ptyBackend) SendProcessInput(context.Context, sandbox.ProcessReference, []byte) error {
	return errors.New("terminal input belongs on the PTY branch")
}
func (b *ptyBackend) ResizePTY(_ context.Context, r sandbox.ProcessReference, rows, cols int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sizes[r.Handle] = [2]int{rows, cols}
	return nil
}
func (b *ptyBackend) SignalProcess(_ context.Context, _ sandbox.ProcessReference, signal int32) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.signals = append(b.signals, signal)
	return nil
}
func (b *ptyBackend) CloseProcessInput(context.Context, sandbox.ProcessReference) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.eof++
	return nil
}

// fakeLifecycleBackend provisions without an engine. Lifecycle correctness is the
// S02 contract's subject; here it only has to reach active.
type fakeLifecycleBackend struct {
	mu        sync.Mutex
	volumes   int
	sandboxes map[string]sandbox.Observation
}

func (f *fakeLifecycleBackend) Identity(context.Context) (sandbox.Identity, error) {
	return sandbox.Identity{StoreID: "engine", Version: "test", ManualColdRecovery: true}, nil
}
func (f *fakeLifecycleBackend) CreateVolume(_ context.Context, key string, capacity int64) (sandbox.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes++
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: "engine", VolumeID: fmt.Sprintf("v%d", f.volumes)}, Capacity: capacity}, nil
}
func (f *fakeLifecycleBackend) Create(_ context.Context, r sandbox.CreateRequest) (sandbox.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sandboxes == nil {
		f.sandboxes = map[string]sandbox.Observation{}
	}
	o, ok := f.sandboxes[r.IdempotencyKey]
	if !ok {
		o = sandbox.Observation{Reference: sandbox.Reference{StoreID: "engine", SandboxID: "box", MutationRevision: "r1", Epoch: 1}, State: "running", PlacementState: "active"}
		f.sandboxes[r.IdempotencyKey] = o
	}
	return o, nil
}
func (f *fakeLifecycleBackend) Inspect(_ context.Context, ref sandbox.Reference) (sandbox.Observation, error) {
	return sandbox.Observation{Reference: ref, State: "running", PlacementState: "active"}, nil
}
func (f *fakeLifecycleBackend) Pause(_ context.Context, ref sandbox.Reference) (sandbox.Observation, error) {
	o := sandbox.Observation{Reference: ref, State: "paused", PlacementState: "lost"}
	return o, nil
}
func (f *fakeLifecycleBackend) Resume(_ context.Context, ref sandbox.Reference) (sandbox.Observation, error) {
	ref.Epoch++
	return sandbox.Observation{Reference: ref, State: "running", PlacementState: "active"}, nil
}
func (f *fakeLifecycleBackend) Destroy(context.Context, sandbox.Reference) error { return nil }
func (f *fakeLifecycleBackend) DeleteVolume(context.Context, sandbox.VolumeReference) error {
	return nil
}
func (f *fakeLifecycleBackend) WriteProjection(context.Context, sandbox.Reference, string, []byte) error {
	return nil
}

type harness struct {
	server    *httptest.Server
	store     *product.Store
	terminals *product.Terminals
	lifecycle *product.Lifecycle
	backend   *ptyBackend
	actor     string
	mu        sync.Mutex
}

// converge runs the lifecycle worker to completion. The transport deliberately
// does not provision inside a request: starting a terminal reports pending until
// the Project placement exists, which is what a deployment's worker resolves.
func (h *harness) converge(t *testing.T, project string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		err := h.lifecycle.Reconcile(ctx, project)
		if errors.Is(err, product.ErrNotFound) {
			return
		}
		must(t, err)
	}
	t.Fatal("lifecycle did not converge")
}

func (h *harness) setActor(actor string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actor = actor
}
func (h *harness) currentActor() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.actor
}

func testProfile() product.Profile {
	return product.Profile{StoreID: "engine", TemplateID: "battery", CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 1 << 30, WorkspaceBytes: 1 << 30, RuntimeBytes: 1 << 29}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "product.db"))
	must(t, err)
	t.Cleanup(func() { store.Close() })
	lifecycle := &product.Lifecycle{Store: store, Backend: &fakeLifecycleBackend{}, MaxProjects: 4}
	backend := newPTYBackend()
	terminals := product.NewTerminals(ctx, store, lifecycle, backend, testProfile(), 8)
	t.Cleanup(terminals.Close)
	h := &harness{store: store, terminals: terminals, lifecycle: lifecycle, backend: backend, actor: "alice"}
	s := &Server{
		Store: store, Terminals: terminals, Lifecycle: lifecycle, Profile: testProfile(),
		Auth: AuthenticatorFunc(func(*http.Request) (string, error) {
			if actor := h.currentActor(); actor != "" {
				return actor, nil
			}
			return "", errors.New("no session")
		}),
	}
	h.server = httptest.NewServer(s.Handler())
	t.Cleanup(s.Close)
	t.Cleanup(h.server.Close)
	return h
}

// do issues a request with the deployment's own origin, the way a browser on the
// same origin would.
func (h *harness) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	return h.origin(t, method, path, body, h.server.URL)
}

func (h *harness) origin(t *testing.T, method, path, body, origin string) (int, []byte) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request, err := http.NewRequestWithContext(ctx, method, h.server.URL+path, reader)
	must(t, err)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := h.server.Client().Do(request)
	must(t, err)
	defer response.Body.Close()
	raw := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}
	return response.StatusCode, raw
}

func (h *harness) expect(t *testing.T, method, path, body string, want int) []byte {
	t.Helper()
	code, raw := h.do(t, method, path, body)
	if code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, raw)
	}
	return raw
}

// project creates an active Project with one running terminal, and returns the
// terminal's live incarnation.
func (h *harness) terminal(t *testing.T, project, terminal string) product.TerminalIncarnation {
	t.Helper()
	h.expect(t, "POST", "/api/projects", fmt.Sprintf(`{"ID":%q,"Title":"Project"}`, project), 201)
	h.expect(t, "POST", "/api/projects/"+project+"/terminals", fmt.Sprintf(`{"id":%q,"title":"Terminal","rows":24,"cols":80}`, terminal), 201)
	scope := product.Scope{Actor: h.currentActor(), ProjectID: project}
	// The first start records provisioning intent and reports pending. A request
	// never waits on remote work, so the worker converges the placement here.
	code, body := h.do(t, "POST", fmt.Sprintf("/api/projects/%s/terminals/%s/start", project, terminal), "")
	if code != http.StatusAccepted {
		t.Fatalf("first start must report pending while the Project provisions: %d %s", code, body)
	}
	h.converge(t, project)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, _ := h.do(t, "POST", fmt.Sprintf("/api/projects/%s/terminals/%s/start", project, terminal), "")
		if code == http.StatusOK || code == http.StatusAccepted {
			if i, err := h.store.LiveTerminal(ctx, scope, terminal); err == nil {
				if s := h.backend.stream(i.Reference.Handle); s != nil && i.State == "starting" {
					s.frames <- sandbox.ProcessFrame{Kind: "start", Sequence: 1}
				}
				if i.State == "running" {
					return i
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("terminal did not reach running through the HTTP surface")
	return product.TerminalIncarnation{}
}

func (h *harness) socket(t *testing.T, path string, origin string) *websocket.Conn {
	t.Helper()
	target := "ws" + strings.TrimPrefix(h.server.URL, "http") + path
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, target, header)
	if err != nil {
		if response != nil {
			t.Fatalf("dial %s: %v (HTTP %d)", path, err, response.StatusCode)
		}
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readText(t *testing.T, conn *websocket.Conn, match func(map[string]any) bool) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.TextMessage {
			continue
		}
		var frame map[string]any
		must(t, json.Unmarshal(data, &frame))
		if match(frame) {
			return frame
		}
	}
}

func TestProductAPIRequiresIdentityAndSameOrigin(t *testing.T) {
	h := newHarness(t)
	h.expect(t, "POST", "/api/projects", `{"ID":"p","Title":"Project"}`, 201)

	// A cross-origin write is refused before authentication or any store call,
	// because this is the CSRF boundary the Node proxy used to hold.
	code, _ := h.origin(t, "POST", "/api/projects", `{"ID":"forged","Title":"Forged"}`, "https://evil.example")
	if code != http.StatusForbidden {
		t.Fatal(code)
	}
	// A cross-origin read is also refused for the WebSocket handshake, checked
	// separately below; ordinary GETs carry no ambient write authority.
	code, _ = h.origin(t, "GET", "/api/projects", "", "https://evil.example")
	if code != http.StatusOK {
		t.Fatal("a cross-origin read of a cookie-authorized listing is the browser's own concern", code)
	}
	if _, raw := h.do(t, "GET", "/api/projects/forged", ""); !strings.Contains(string(raw), "not found") {
		t.Fatal("the refused write must not have created anything", string(raw))
	}

	// No identity means no access, and no information about what exists.
	h.setActor("")
	code, _ = h.do(t, "GET", "/api/projects/p", "")
	if code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	// A different actor sees not-found rather than forbidden: a client must not be
	// able to probe for another owner's Projects.
	h.setActor("bob")
	code, _ = h.do(t, "GET", "/api/projects/p", "")
	if code != http.StatusNotFound {
		t.Fatal(code)
	}
	code, _ = h.do(t, "POST", "/api/projects/p/terminals", `{"id":"t","title":"T"}`)
	if code != http.StatusNotFound {
		t.Fatal(code)
	}
}

func TestProductAPIRejectsMalformedAndOversizedBodies(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{`{"ID":"p","Title":"P"} {"ID":"q"}`, `{"unknown":true}`, `not json`} {
		code, _ := h.do(t, "POST", "/api/projects", body)
		if code != http.StatusBadRequest {
			t.Fatalf("%q: %d", body, code)
		}
	}
	oversized := `{"ID":"p","Title":"` + strings.Repeat("x", 2<<20) + `"}`
	code, _ := h.do(t, "POST", "/api/projects", oversized)
	if code != http.StatusBadRequest {
		t.Fatal(code)
	}
}

func TestTerminalSocketFanOutOwnershipAndControl(t *testing.T) {
	h := newHarness(t)
	i := h.terminal(t, "p", "shell")
	stream := h.backend.stream(i.Reference.Handle)

	// A foreign origin cannot open the socket: an upgrade from a hostile page
	// would otherwise carry the victim's cookie into a live keyboard.
	target := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/api/projects/p/terminals/shell/attach?input=1"
	_, response, err := websocket.DefaultDialer.DialContext(ctx, target, http.Header{"Origin": []string{"https://evil.example"}})
	if err == nil {
		t.Fatal("a cross-origin upgrade must be refused")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal(response)
	}

	writer := h.socket(t, "/api/projects/p/terminals/shell/attach?input=1", h.server.URL)
	readText(t, writer, func(f map[string]any) bool { return f["kind"] == "status" && f["input"] == true })
	reader := h.socket(t, "/api/projects/p/terminals/shell/attach?input=1", h.server.URL)
	readText(t, reader, func(f map[string]any) bool { return f["kind"] == "status" && f["input"] != true })

	// Output reaches both views as raw binary frames.
	stream.frames <- sandbox.ProcessFrame{Kind: "data", Stream: "pty", Data: []byte("hello\r\n"), Sequence: 2}
	for _, conn := range []*websocket.Conn{writer, reader} {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			kind, data, err := conn.ReadMessage()
			must(t, err)
			if kind == websocket.BinaryMessage {
				if string(data) != "hello\r\n" {
					t.Fatalf("raw bytes altered: %q", data)
				}
				break
			}
		}
	}

	// Only the input holder's keystrokes reach the PTY.
	must(t, writer.WriteMessage(websocket.BinaryMessage, []byte("id\r")))
	must(t, reader.WriteMessage(websocket.BinaryMessage, []byte("rm -rf /\r")))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.backend.mu.Lock()
		got := string(h.backend.input)
		h.backend.mu.Unlock()
		if got == "id\r" {
			break
		}
		if strings.Contains(got, "rm -rf") {
			t.Fatal("a read-only observer must not reach the PTY", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.backend.mu.Lock()
	sent := string(h.backend.input)
	h.backend.mu.Unlock()
	if sent != "id\r" {
		t.Fatalf("input was not exactly the owner's keystrokes: %q", sent)
	}

	// Control frames: resize converges, signals are restricted, EOF is a request.
	must(t, writer.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","rows":40,"cols":120}`)))
	readText(t, writer, func(f map[string]any) bool { return f["kind"] == "status" && f["rows"] == float64(40) })
	// Drain the observer's resize status before waiting for the distinct detach
	// status below. Both are read-only statuses; consuming the former after close
	// can race input claim against the old writer's actual detachment.
	readText(t, reader, func(f map[string]any) bool { return f["kind"] == "status" && f["rows"] == float64(40) })
	must(t, writer.WriteMessage(websocket.TextMessage, []byte(`{"type":"signal","signal":15}`)))
	must(t, writer.WriteMessage(websocket.TextMessage, []byte(`{"type":"signal","signal":99}`)))
	must(t, writer.WriteMessage(websocket.TextMessage, []byte(`{"type":"eof"}`)))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.backend.mu.Lock()
		signals, eof := append([]int32(nil), h.backend.signals...), h.backend.eof
		h.backend.mu.Unlock()
		if len(signals) == 1 && signals[0] == 15 && eof == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.backend.mu.Lock()
	signals := append([]int32(nil), h.backend.signals...)
	h.backend.mu.Unlock()
	for _, signal := range signals {
		if signal != 15 && signal != 9 {
			t.Fatal("an arbitrary signal must not be expressible from a browser", signals)
		}
	}

	// Closing a view detaches one observer. The PTY keeps running and the Project
	// stays awake: that is the retained-terminal rule.
	writer.Close()
	readText(t, reader, func(f map[string]any) bool { return f["kind"] == "status" && f["input"] != true })
	live, err := h.store.LiveTerminal(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, "shell")
	must(t, err)
	if live.State != "running" {
		t.Fatal("closing a browser view must not stop the PTY", live)
	}
	_, err = h.store.ArmIdle(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, time.Second)
	if !errors.Is(err, product.ErrBusy) {
		t.Fatal("a retained live PTY must keep the Project awake", err)
	}

	// The remaining view can take input deliberately, and then drive the terminal.
	must(t, reader.WriteMessage(websocket.TextMessage, []byte(`{"type":"claim_input"}`)))
	readText(t, reader, func(f map[string]any) bool { return f["kind"] == "status" && f["input"] == true })
	must(t, reader.WriteMessage(websocket.BinaryMessage, []byte("now-me\r")))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.backend.mu.Lock()
		got := string(h.backend.input)
		h.backend.mu.Unlock()
		if strings.HasSuffix(got, "now-me\r") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the claiming view could not drive the terminal")
}

func TestTerminalSocketReportsGapAndExit(t *testing.T) {
	h := newHarness(t)
	i := h.terminal(t, "p", "shell")
	stream := h.backend.stream(i.Reference.Handle)
	conn := h.socket(t, "/api/projects/p/terminals/shell/attach?input=1", h.server.URL)
	readText(t, conn, func(f map[string]any) bool { return f["kind"] == "status" })

	// A gap is explicit so the renderer resets rather than drawing a corrupted
	// screen out of bytes it never received.
	stream.frames <- sandbox.ProcessFrame{Kind: "data", Gap: true, ResetReason: "replay ring exceeded", Sequence: 5}
	gap := readText(t, conn, func(f map[string]any) bool { return f["kind"] == "gap" })
	if gap["reason"] != "replay ring exceeded" {
		t.Fatal(gap)
	}

	// The exit code comes from the process's terminal frame and is projected once.
	stream.frames <- sandbox.ProcessFrame{Kind: "end", ExitCode: 130, Sequence: 6}
	exit := readText(t, conn, func(f map[string]any) bool { return f["kind"] == "exit" })
	if exit["exit_code"] != float64(130) {
		t.Fatal(exit)
	}
	raw := h.expect(t, "GET", "/api/projects/p/terminals/shell", "", 200)
	var child product.Terminal
	must(t, json.Unmarshal(raw, &child))
	if child.State != "exited" || child.ExitCode == nil || *child.ExitCode != 130 {
		t.Fatal(child)
	}
	// An ended terminal stops pinning the Project but keeps its metadata.
	_, err := h.store.ArmIdle(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, time.Second)
	must(t, err)
}

func TestTerminalSocketClosesWhenAuthorizationEnds(t *testing.T) {
	h := newHarness(t)
	h.terminal(t, "p", "shell")
	conn := h.socket(t, "/api/projects/p/terminals/shell/attach", h.server.URL)
	readText(t, conn, func(f map[string]any) bool { return f["kind"] == "status" })

	// Attaching to a deleting terminal is refused outright.
	raw := h.expect(t, "GET", "/api/projects/p/terminals/shell", "", 200)
	var child product.Terminal
	must(t, json.Unmarshal(raw, &child))
	h.expect(t, "DELETE", fmt.Sprintf("/api/projects/p/terminals/shell?revision=%d", child.Revision), "", 202)
	code, _ := h.do(t, "GET", "/api/projects/p/terminals/shell/attach", "")
	if code != http.StatusConflict {
		t.Fatal("a deleting terminal must refuse new attachments", code)
	}
	// Deleting the terminal requested termination but did not fabricate an exit.
	live, err := h.store.LiveTerminal(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, "shell")
	must(t, err)
	if live.State != "running" {
		t.Fatal("deletion must not claim a process ended", live)
	}
	// Termination is requested and then escalated, because an interactive shell
	// ignores SIGTERM. Neither signal records an exit: only the process's own
	// terminal frame does, and this scripted backend never sends one.
	h.backend.mu.Lock()
	signals := append([]int32(nil), h.backend.signals...)
	h.backend.mu.Unlock()
	if len(signals) == 0 || signals[0] != 15 {
		t.Fatalf("deletion must request termination first: %v", signals)
	}
	for _, signal := range signals {
		if signal != 15 && signal != 9 {
			t.Fatalf("only termination signals may be delivered: %v", signals)
		}
	}
}

func TestProductEventsStreamIsScopedAndReplayable(t *testing.T) {
	h := newHarness(t)
	h.expect(t, "POST", "/api/projects", `{"ID":"p","Title":"Project"}`, 201)
	h.expect(t, "POST", "/api/projects/p/sessions", `{"ID":"s","Title":"Agent"}`, 201)
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, frontier, err := h.store.EventBounds(ctx, scope)
	must(t, err)
	event := func(path string) string {
		t.Helper()
		stream, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(stream, "GET", h.server.URL+path, nil)
		must(t, err)
		response, err := h.server.Client().Do(request)
		must(t, err)
		defer response.Body.Close()
		if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatal(response.StatusCode)
		}
		scanner := bufio.NewScanner(response.Body)
		var lines []string
		for scanner.Scan() {
			if scanner.Text() == "" {
				break
			}
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	// A new observer receives a frontier and refetches snapshots, without replaying
	// all historical invalidations. A reconnect still replays after its own cursor.
	initial := event("/api/projects/p/events")
	if !strings.Contains(initial, "event: snapshot") || !strings.Contains(initial, fmt.Sprintf(`"cursor":%d`, frontier)) {
		t.Fatal(initial)
	}
	child, err := h.store.Session(ctx, scope, "s")
	must(t, err)
	must(t, h.store.RenameSession(ctx, scope, "s", child.Revision, "New title"))
	replay := event(fmt.Sprintf("/api/projects/p/events?after=%d", frontier))
	if !strings.Contains(replay, "event: projection") || !strings.Contains(replay, "session.updated") || strings.Contains(replay, "terminal.output") {
		t.Fatal(replay)
	}
	reset := event("/api/projects/p/events?after=999999")
	if !strings.Contains(reset, "event: gap") {
		t.Fatal(reset)
	}
	h.setActor("bob")
	code, _ := h.do(t, "GET", "/api/projects/p/events", "")
	if code != 404 {
		t.Fatal(code)
	}
}

func TestProjectPauseRejectsLiveTerminalAndReportsBlocker(t *testing.T) {
	h := newHarness(t)
	h.terminal(t, "p", "shell")
	raw := h.expect(t, "GET", "/api/projects/p", "", 200)
	var p product.Project
	must(t, json.Unmarshal(raw, &p))
	code, body := h.do(t, "POST", fmt.Sprintf("/api/projects/p/pause?revision=%d", p.Revision), "")
	if code != http.StatusConflict {
		t.Fatal(code, string(body))
	}
	raw = h.expect(t, "GET", "/api/projects/p/blockers", "", 200)
	if !strings.Contains(string(raw), "terminal_process") {
		t.Fatal("the live PTY must be a named, understandable idle blocker", string(raw))
	}
}

var _ = url.Parse

func (f *ptyBackend) InspectProcess(_ context.Context, ref sandbox.ProcessReference) (sandbox.ProcessObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.streams[ref.Handle] == nil {
		return sandbox.ProcessObservation{}, &sandbox.Error{Class: sandbox.NotFound}
	}
	return sandbox.ProcessObservation{ProcessReference: ref, State: "running"}, nil
}
