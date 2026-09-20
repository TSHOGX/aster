package server

import (
	"harness-platform/orchestrator/internal/agents"

	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/events"
)

func TestEventsStreamReturnsWhenApplicationShutdownBegins(t *testing.T) {
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		hub:        events.NewHub(),
		shutdownCh: make(chan struct{}),
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.eventsStream))
	defer httpSrv.Close()

	resp, err := http.Get(httpSrv.URL)
	if err != nil {
		t.Fatalf("connect to events stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events stream status=%d want 200", resp.StatusCode)
	}
	// Consume the initial frame before waiting for the application shutdown
	// signal. Without this, a buffered transport can hide that the handler has
	// actually entered its long-lived select loop.
	initial := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(resp.Body, initial); err != nil {
		t.Fatalf("read initial stream frame: %v", err)
	}

	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		readDone <- err
	}()
	srv.BeginShutdown()

	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("events stream handler did not return after BeginShutdown")
	}
}
