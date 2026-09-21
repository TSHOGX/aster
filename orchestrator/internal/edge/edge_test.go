package edge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	must(t, err)
	return u
}

func TestEdgeRoutesAPIAndFrontendSeparately(t *testing.T) {
	application := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "application")
		w.Write([]byte(r.URL.Path))
	}))
	defer application.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "frontend")
		w.Write([]byte(r.URL.Path))
	}))
	defer frontend.Close()

	handler, err := New(Config{Application: mustURL(t, application.URL), Frontend: mustURL(t, frontend.URL)})
	must(t, err)
	origin := httptest.NewServer(handler)
	defer origin.Close()

	for path, want := range map[string]string{
		"/api/projects":                      "application",
		"/api/projects/p/terminals/t/attach": "application",
		"/":                                  "frontend",
		"/workbench":                         "frontend",
		"/apifoo":                            "frontend",
	} {
		response, err := origin.Client().Get(origin.URL + path)
		must(t, err)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if got := response.Header.Get("X-Upstream"); got != want {
			t.Fatalf("%s routed to %q, want %q", path, got, want)
		}
		if string(body) != path {
			t.Fatalf("%s arrived upstream as %q", path, body)
		}
	}
}

// TestEdgePreservesOriginAndForwardedHost is the point of this package. The
// application is the origin authority, so it must receive the browser's real
// Origin and a forwarded host naming the public origin rather than this hop.
func TestEdgePreservesOriginAndForwardedHost(t *testing.T) {
	seen := make(chan http.Header, 4)
	application := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer application.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer frontend.Close()

	handler, err := New(Config{Application: mustURL(t, application.URL), Frontend: mustURL(t, frontend.URL)})
	must(t, err)
	origin := httptest.NewServer(handler)
	defer origin.Close()

	request, err := http.NewRequestWithContext(ctx, "POST", origin.URL+"/api/projects", strings.NewReader("{}"))
	must(t, err)
	request.Header.Set("Origin", origin.URL)
	// A hostile client's forwarding claims must not survive this hop.
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("X-Forwarded-Host", "evil.example")
	response, err := origin.Client().Do(request)
	must(t, err)
	response.Body.Close()

	header := <-seen
	if header.Get("Origin") != origin.URL {
		t.Fatalf("the application must see the browser's Origin, got %q", header.Get("Origin"))
	}
	publicHost := strings.TrimPrefix(origin.URL, "http://")
	if header.Get("X-Forwarded-Host") != publicHost {
		t.Fatalf("forwarded host must name the public origin, got %q", header.Get("X-Forwarded-Host"))
	}
	if strings.HasPrefix(header.Get("X-Forwarded-For"), "203.0.113.9") {
		t.Fatalf("a client-supplied forwarding chain must not be trusted: %q", header.Get("X-Forwarded-For"))
	}
	if header.Get("X-Real-IP") == "" {
		t.Fatal("the observed peer must be forwarded")
	}
}

// TestEdgeCarriesWebSocketUpgrade is the capability the Next.js fetch proxy
// cannot provide, and the reason terminals need this hop at all.
func TestEdgeCarriesWebSocketUpgrade(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	application := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Echo the Origin the application saw, so the test proves it survived.
		_ = conn.WriteMessage(websocket.TextMessage, []byte(r.Header.Get("Origin")))
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err = conn.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}))
	defer application.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer frontend.Close()

	handler, err := New(Config{Application: mustURL(t, application.URL), Frontend: mustURL(t, frontend.URL)})
	must(t, err)
	origin := httptest.NewServer(handler)
	defer origin.Close()

	target := "ws" + strings.TrimPrefix(origin.URL, "http") + "/api/projects/p/terminals/t/attach"
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, target, http.Header{"Origin": []string{origin.URL}})
	must(t, err)
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	must(t, err)
	if string(data) != origin.URL {
		t.Fatalf("the application must see the browser's Origin through the upgrade, got %q", data)
	}
	// Raw binary frames survive the hop unchanged, which is what terminal bytes
	// and keystrokes are.
	payload := []byte("\x1b[1m世\x07\x00\xff")
	must(t, conn.WriteMessage(websocket.BinaryMessage, payload))
	kind, echoed, err := conn.ReadMessage()
	must(t, err)
	if kind != websocket.BinaryMessage || string(echoed) != string(payload) {
		t.Fatalf("binary frame altered in transit: %q", echoed)
	}
}

// TestEdgeStreamsWithoutBuffering pins the flush behavior. Without it an event
// stream or terminal sits in a buffer until it fills, which is indistinguishable
// from a hung terminal.
func TestEdgeStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	application := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("event: first\ndata: 1\n\n"))
		flusher.Flush()
		<-release
		w.Write([]byte("event: second\ndata: 2\n\n"))
		flusher.Flush()
	}))
	defer application.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer frontend.Close()

	handler, err := New(Config{Application: mustURL(t, application.URL), Frontend: mustURL(t, frontend.URL)})
	must(t, err)
	origin := httptest.NewServer(handler)
	defer origin.Close()

	stream, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(stream, "GET", origin.URL+"/api/projects/p/events", nil)
	must(t, err)
	response, err := origin.Client().Do(request)
	must(t, err)
	defer response.Body.Close()
	// read accumulates until the wanted marker appears. A short read is normal on a
	// streamed body; only silence would mean the proxy buffered.
	read := func(want string) {
		t.Helper()
		buf := make([]byte, 256)
		var seen strings.Builder
		for !strings.Contains(seen.String(), want) {
			n, err := response.Body.Read(buf)
			seen.Write(buf[:n])
			// A final read can deliver bytes and EOF together, so the marker is
			// rechecked before the error is treated as a failure.
			if err != nil && !strings.Contains(seen.String(), want) {
				t.Fatalf("stream ended before %q arrived: %q (%v)", want, seen.String(), err)
			}
		}
	}
	// "first" must arrive while the upstream is still blocked on the second write.
	read("first")
	close(release)
	read("second")
}

func TestEdgeRefusesIncompleteConfigurationAndReportsBadGateway(t *testing.T) {
	frontend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer frontend.Close()
	for _, config := range []Config{
		{},
		{Application: mustURL(t, "http://127.0.0.1:1")},
		{Application: mustURL(t, "ftp://host"), Frontend: mustURL(t, frontend.URL)},
		{Application: mustURL(t, "http://"), Frontend: mustURL(t, frontend.URL)},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("a half-configured origin must be refused: %+v", config)
		}
	}
	// An unreachable upstream is a gateway error, never a frontend page that looks
	// like a missing route.
	handler, err := New(Config{Application: mustURL(t, "http://127.0.0.1:1"), Frontend: mustURL(t, frontend.URL)})
	must(t, err)
	origin := httptest.NewServer(handler)
	defer origin.Close()
	response, err := origin.Client().Get(origin.URL + "/api/projects")
	must(t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatal(response.StatusCode)
	}
}
