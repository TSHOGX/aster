package producthttp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
)

func isolatedHub() *resourceHub {
	return &resourceHub{scope: product.Scope{Actor: "actor", ProjectID: "project"}, incarnation: "incarnation", subscribers: map[*resourceSubscription]struct{}{}, cancel: func() {}}
}
func nextResource(t *testing.T, sub *resourceSubscription) resourceFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := sub.next(ctx)
	must(t, err)
	return frame
}
func TestResourceReplayEmptyScopeRotationsAndResync(t *testing.T) {
	h := isolatedHub()
	initial, err := h.attach("")
	must(t, err)
	reset := nextResource(t, initial)
	if reset.event.Kind != "resync" || reset.event.Reason != "initial" {
		t.Fatal(reset.event)
	}
	ready := nextResource(t, initial)
	if ready.event.Kind != "ready" {
		t.Fatal(ready.event)
	}
	for n := 0; n < 2; n++ {
		resumed, e := h.attach(ready.event.Cursor)
		must(t, e)
		if event := nextResource(t, resumed).event; event.Kind != "ready" {
			t.Fatal("empty rotation invented a file change", event)
		}
	}
	h.publish("files", "", "7", "", sandbox.FileInvalidation{Path: "report.txt", Epoch: 2, Sequence: 7, Operation: "write"})
	replay, err := h.attach(ready.event.Cursor)
	must(t, err)
	file := nextResource(t, replay)
	if file.event.Kind != "files" {
		t.Fatal(file.event)
	}
	h.publish("content", "s", "9007199254740993", "conversation.view", nil)
	next, err := h.attach(file.event.Cursor)
	must(t, err)
	content := nextResource(t, next)
	if content.event.Revision != "9007199254740993" {
		t.Fatal(content.event)
	}
	restarted := isolatedHub()
	restarted.incarnation = "replacement"
	afterRestart, err := restarted.attach(content.event.Cursor)
	must(t, err)
	if e := nextResource(t, afterRestart).event; e.Reason != "hub_restart" {
		t.Fatal(e)
	}
	h.mu.Lock()
	for n := range h.ring {
		h.ring[n].at = time.Now().Add(-2 * time.Minute)
	}
	h.mu.Unlock()
	expired, err := h.attach(ready.event.Cursor)
	must(t, err)
	if e := nextResource(t, expired).event; e.Reason != "replay_expired" {
		t.Fatal(e)
	}
	other := isolatedHub()
	other.scope.ProjectID = "other"
	if _, err = other.attach(file.event.Cursor); err != product.ErrInvalid {
		t.Fatal("foreign cursor accepted", err)
	}
}
func TestSlowResourceSubscriberCannotBlockFastReader(t *testing.T) {
	h := isolatedHub()
	fast, err := h.attach("")
	must(t, err)
	slow, err := h.attach("")
	must(t, err)
	for _, sub := range []*resourceSubscription{fast, slow} {
		nextResource(t, sub)
		nextResource(t, sub)
	}
	for n := 0; n < 1000; n++ {
		h.publish("content", "session", "1", "conversation.view", nil)
		if e := nextResource(t, fast).event; e.Kind != "content" {
			t.Fatal(e)
		}
	}
	h.mu.Lock()
	size, bytes, closed := len(slow.queue), slow.bytes, slow.closed
	h.mu.Unlock()
	if size > subscriberCount || bytes > subscriberBytes || !closed {
		t.Fatal(size, bytes, closed)
	}
	if e := nextResource(t, slow).event; e.Kind != "resync" || e.Reason != "slow_consumer" {
		t.Fatal(e)
	}
	if _, err = slow.next(ctx); err != io.EOF {
		t.Fatal(err)
	}
}
func TestResourceScopeClassification(t *testing.T) {
	for _, c := range []struct{ event, kind string }{
		{"session.control", "control"}, {"conversation.view", "content"},
		{"session.title", "sessions"}, {"terminal.exited", "terminals"}, {"project.placement_lost", "project"},
		{"conversation.revoked", "authorization"}, {"future.event", "resync"},
	} {
		kind, _ := resourceKind(product.Event{Kind: c.event})
		if kind != c.kind {
			t.Fatal(c, kind)
		}
	}
}

func TestResourceFiftyConsumerCyclesReleaseAuthority(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	s := &Server{Store: h.store}
	s.ResourceHubStats()
	for n := 0; n < 50; n++ {
		sub, e := s.hubs.attach(scope, "")
		must(t, e)
		nextResource(t, sub)
		nextResource(t, sub)
		s.hubs.detach(sub)
	}
	stats := s.ResourceHubStats()
	if stats.Hubs != 1 || stats.Subscribers != 0 || stats.QueuedBytes != 0 || stats.Watchers != 0 {
		t.Fatal(stats)
	}
	p, err := h.store.Project(ctx, scope)
	must(t, err)
	if p.State != "created" {
		t.Fatal("hub activated a Project", p.State)
	}
	s.Close()
	stats = s.ResourceHubStats()
	if stats.Hubs != 0 || stats.Subscribers != 0 || stats.Watchers != 0 {
		t.Fatal(stats)
	}
}
func TestResourceHTTPReplayAuthorityAndCleanup(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	url := h.server.URL + "/api/projects/p/resources/events"
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	must(t, err)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	read := func() *ResourceEvent {
		t.Helper()
		for {
			line, e := reader.ReadString('\n')
			must(t, e)
			if strings.HasPrefix(line, "data: ") {
				var event ResourceEvent
				must(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &event))
				return &event
			}
		}
	}
	if e := read(); e.Kind != "resync" {
		t.Fatal(e)
	}
	ready := read()
	if ready.Kind != "ready" {
		t.Fatal(ready)
	}
	_, err = h.store.CreateSession(ctx, scope, "s", "Session")
	must(t, err)
	event := read()
	if event.Kind != "sessions" || event.ID != "s" {
		t.Fatal(event)
	}
	response.Body.Close()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Last-Event-ID", event.Cursor)
	response, err = client.Do(req)
	must(t, err)
	defer response.Body.Close()
	reader = bufio.NewReader(response.Body)
	for {
		e := read()
		if e.Kind == "ready" {
			break
		}
		if e.Kind == "resync" || e.Kind == "files" {
			t.Fatal("rotation resynced stable hub", e)
		}
	}
	h.setActor("bob")
	// Revocation closes this stream; the same URL is no longer authorized.
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			break
		}
		if strings.HasPrefix(line, "event: closed") {
			break
		}
	}
	response.Body.Close()
	response, err = client.Get(url)
	must(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal(response.StatusCode)
	}
}

func TestScheduleOwnerHubReplaysWithoutProjectFanout(t *testing.T) {
	h := newHarness(t)
	server := &Server{Store: h.store, Auth: AuthenticatorFunc(func(*http.Request) (string, error) { return "alice", nil })}
	defer server.Close()
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	server.hubsOnce.Do(func() { server.hubs = newResourceHubs(server) })
	owner := product.Scope{Actor: "alice"}
	sub, err := server.hubs.attach(owner, "")
	must(t, err)
	defer server.hubs.detach(sub)
	if nextResource(t, sub).event.Kind != "resync" {
		t.Fatal("initial state must reconcile")
	}
	ready := nextResource(t, sub).event
	_, err = h.store.CreateSchedule(ctx, scope, product.Schedule{ID: "task", Title: "Task", Prompt: "private prompt", Timezone: "UTC", Enabled: true})
	must(t, err)
	event := nextResource(t, sub).event
	if event.Kind != "schedule.created" || event.ID != "task" {
		t.Fatal(event)
	}
	raw, _ := json.Marshal(event)
	if strings.Contains(string(raw), "private prompt") {
		t.Fatal("prompt leaked")
	}
	replay, err := server.hubs.attach(owner, ready.Cursor)
	must(t, err)
	defer server.hubs.detach(replay)
	if nextResource(t, replay).event.Kind != "schedule.created" {
		t.Fatal("missing replay")
	}
	stats := server.ResourceHubStats()
	if stats.Hubs != 1 || stats.Watchers != 0 {
		t.Fatal("owner stream fans out", stats)
	}
	foreign := product.Scope{Actor: "bob"}
	if _, err := server.hubs.attach(foreign, event.Cursor); err != product.ErrInvalid {
		t.Fatal("foreign cursor accepted", err)
	}
}

func TestScheduleHTTPHintsAuthorizationAndForeignCursor(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, err := h.store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	_, err = h.store.CreateProject(ctx, "bob", "foreign", "Foreign")
	must(t, err)
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Get(h.server.URL + "/api/schedules/events")
	must(t, err)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	read := func() ResourceEvent {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			must(t, err)
			if strings.HasPrefix(line, "data: ") {
				var e ResourceEvent
				must(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &e))
				return e
			}
		}
	}
	if read().Kind != "resync" || read().Kind != "ready" {
		t.Fatal("missing bootstrap handshake")
	}
	_, err = h.store.CreateSchedule(ctx, product.Scope{Actor: "bob", ProjectID: "foreign"}, product.Schedule{ID: "secret", Title: "Secret", Prompt: "secret", Timezone: "UTC", Enabled: true})
	must(t, err)
	_, err = h.store.CreateSchedule(ctx, scope, product.Schedule{ID: "task", Title: "Task", Prompt: "private", Timezone: "UTC", Enabled: true})
	must(t, err)
	event := read()
	if event.Kind != "schedule.created" || event.ID != "task" {
		t.Fatal("foreign hint leaked", event)
	}
	response.Body.Close()
	h.setActor("bob")
	response, err = client.Get(h.server.URL + "/api/schedules/events?cursor=" + event.Cursor)
	must(t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("foreign owner cursor accepted", response.StatusCode)
	}
}
