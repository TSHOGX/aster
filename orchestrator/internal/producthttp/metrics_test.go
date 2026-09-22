package producthttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDiagnosticsStreamingAndSocket(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			observed := make(chan HTTPObservation, 4)
			s := &Server{}
			if enabled {
				s.Config.ObserveHTTP = func(o HTTPObservation) { observed <- o }
			}
			release := make(chan struct{})
			defer close(release)
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if RequestID(r.Context()) == "" || RequestID(r.Context()) == "untrusted" {
					t.Error("missing server correlation")
				}
				if strings.HasSuffix(r.URL.Path, "/attach") {
					c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer c.Close()
					_ = c.WriteMessage(websocket.TextMessage, []byte("terminal"))
					return
				}
				if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: live\n\n")
				w.(http.Flusher).Flush()
				<-release
			})
			server := httptest.NewServer(s.diagnostics(h))
			t.Cleanup(server.Close)
			// Close the streaming body before server.Close, then release the handler.
			req, _ := http.NewRequest("GET", server.URL+"/events", nil)
			req.Header.Set("X-Request-ID", "untrusted")
			response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b := make([]byte, len("data: live\n\n"))
			_, err = io.ReadFull(response.Body, b)
			response.Body.Close()
			if err != nil || string(b) != "data: live\n\n" {
				t.Fatalf("stream: %q %v", b, err)
			}
			if response.Header.Get("X-Request-ID") == "" {
				t.Fatal("missing correlation header")
			}
			if (response.Header.Get("Server-Timing") != "") != enabled {
				t.Fatal("timing opt-in failed")
			}
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/attach", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, b, err = c.ReadMessage()
			c.Close()
			if err != nil || string(b) != "terminal" {
				t.Fatalf("socket: %q %v", b, err)
			}
			if enabled {
				select {
				case o := <-observed:
					if o.Category != "terminal" || o.Status != 101 {
						t.Fatalf("%+v", o)
					}
				case <-time.After(time.Second):
					t.Fatal("missing observation")
				}
			}
			release <- struct{}{}
		})
	}
}
