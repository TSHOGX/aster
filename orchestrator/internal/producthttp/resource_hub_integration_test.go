//go:build khoraintegration

package producthttp

import (
	"bufio"
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/producttest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

func TestResourceHubOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated endpoint required")
	}
	runctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	for n := 0; err != nil && n < 100; n++ {
		time.Sleep(100 * time.Millisecond)
		backend, err = khora.New(runctx, khora.Config{Endpoint: endpoint})
	}
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "agent-runtime"
	profile.DiskBytes = 256 << 20
	store, profile, err := producttest.OpenWithRuntime(runctx, filepath.Join(t.TempDir(), "product.db"), profile, backend, nil)
	must(t, err)
	defer store.Close()
	scope := product.Scope{Actor: "owner", ProjectID: "watch-only"}
	lifecycle := &product.Lifecycle{VerifyRuntime: backend.VerifyRuntime, Store: store, Backend: backend, MaxProjects: 1}
	workers := &product.Workers{Store: store, Lifecycle: lifecycle, Interval: 100 * time.Millisecond}
	workerctx, stop := context.WithCancel(runctx)
	done := make(chan struct{})
	go func() { defer close(done); workers.Run(workerctx) }()
	defer func() { stop(); <-done }()
	await := func(t *testing.T, label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("timeout: " + label)
	}
	observed := product.Scope{Actor: scope.Actor, ProjectID: "watch-only"}
	_, err = store.CreateProject(runctx, observed.Actor, observed.ProjectID, "Watch only")
	must(t, err)
	must(t, lifecycle.EnsureActive(runctx, observed, profile.Bindings()))
	await(t, "watch-only active", func() bool { p, e := store.Project(runctx, observed); return e == nil && p.State == "active" })
	transport := &Server{Store: store, Files: &product.Files{Store: store, Lifecycle: lifecycle, Backend: backend, Bindings: profile.Bindings()}, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return scope.Actor, nil })}
	defer transport.Close()
	httpServer := httptest.NewServer(transport.Handler())
	defer httpServer.Close()
	listing, err := http.Get(httpServer.URL + "/api/projects/watch-only/files?path=&limit=100")
	must(t, err)
	var empty struct{ Entries []any }
	must(t, json.NewDecoder(listing.Body).Decode(&empty))
	listing.Body.Close()
	if listing.StatusCode != http.StatusOK || len(empty.Entries) != 0 {
		t.Fatal("watch fixture is not an empty workspace", listing.StatusCode, len(empty.Entries))
	}
	cursor := ""
	var response *http.Response
	for rotation := 0; rotation < 3; rotation++ {
		watchctx, cancel := context.WithTimeout(runctx, 35*time.Second)
		request, _ := http.NewRequestWithContext(watchctx, "GET", httpServer.URL+"/api/projects/watch-only/resources/events", nil)
		if cursor != "" {
			request.Header.Set("Last-Event-ID", cursor)
		}
		response, err = http.DefaultClient.Do(request)
		must(t, err)
		scanner := bufio.NewScanner(response.Body)
		initialObserved := rotation > 0
		rotated := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: rotate") {
				rotated = true
				break
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event ResourceEvent
			must(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
			cursor = event.Cursor
			if rotation > 0 && (event.Kind == "files" || event.Kind == "resync") {
				t.Fatalf("stable rotation invalidated content: %+v", event)
			}
			if event.Kind == "files" {
				payload, _ := event.Payload.(map[string]any)
				if payload["reason"] == "initial" && payload["sequence"] == "0" {
					initialObserved = true
				}
			}
			if rotation == 2 && event.Kind == "ready" {
				break
			}
		}
		must(t, scanner.Err())
		if !initialObserved || rotation < 2 && !rotated {
			t.Fatal("watch/rotation not observed", rotation, initialObserved, rotated)
		}
		stats := transport.ResourceHubStats()
		if stats.Watchers != 1 || stats.Hubs != 1 {
			t.Fatal(stats)
		}
		if rotation < 2 {
			response.Body.Close()
			cancel()
		} else {
			defer cancel()
		}
	}
	defer response.Body.Close()
	// The third subscription remains connected while lifecycle pauses. An
	// advisory watcher must neither add an activity blocker nor wake it again.
	_, err = store.ArmIdle(runctx, observed, time.Minute)
	must(t, err)
	p, err := store.Project(runctx, observed)
	must(t, err)
	must(t, lifecycle.Pause(runctx, observed, p.Revision))
	await(t, "watch-only paused", func() bool { p, e := store.Project(runctx, observed); return e == nil && p.State == "paused" })
	await(t, "watch released", func() bool { return transport.ResourceHubStats().Watchers == 0 })
	p, err = store.Project(runctx, observed)
	must(t, err)
	must(t, store.DeleteProject(runctx, observed, p.Revision))
	await(t, "watch-only deleted", func() bool { p, e := store.Project(runctx, observed); return e == nil && p.State == "deleted" })
}
