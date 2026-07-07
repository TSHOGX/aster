package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/store"
)

func withNotificationTestUser(r *http.Request, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userContextKey{}, user))
}

func newNotificationsTestServer(t *testing.T, st *store.Store) *Server {
	t.Helper()
	return &Server{store: st, hub: events.NewHub(), log: slog.Default()}
}

func TestListNotificationsScopedPerUser(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if err := st.CreateNotification(ctx, store.Notification{ID: "notif_1", Title: "Title", Body: "Body", CreatedAt: now}); err != nil {
		t.Fatalf("create notification: %v", err)
	}
	if err := st.MarkNotificationRead(ctx, "notif_1", "alice", now); err != nil {
		t.Fatalf("mark read for alice: %v", err)
	}

	srv := newNotificationsTestServer(t, st)

	rec := httptest.NewRecorder()
	srv.listNotifications(rec, withNotificationTestUser(httptest.NewRequest(http.MethodGet, "/api/notifications", nil), "alice"))
	var alicePayload struct {
		Notifications []struct {
			ID   string `json:"id"`
			Read bool   `json:"read"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &alicePayload); err != nil {
		t.Fatalf("decode alice response: %v", err)
	}
	if len(alicePayload.Notifications) != 1 || !alicePayload.Notifications[0].Read {
		t.Fatalf("expected alice to see the notification as read: %+v", alicePayload.Notifications)
	}

	rec = httptest.NewRecorder()
	srv.listNotifications(rec, withNotificationTestUser(httptest.NewRequest(http.MethodGet, "/api/notifications", nil), "bob"))
	var bobPayload struct {
		Notifications []struct {
			ID   string `json:"id"`
			Read bool   `json:"read"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bobPayload); err != nil {
		t.Fatalf("decode bob response: %v", err)
	}
	if len(bobPayload.Notifications) != 1 || bobPayload.Notifications[0].Read {
		t.Fatalf("expected bob's read state to be independent of alice's: %+v", bobPayload.Notifications)
	}
}

func TestMarkNotificationReadHandler(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.CreateNotification(ctx, store.Notification{ID: "notif_1", Title: "Title", Body: "Body", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create notification: %v", err)
	}

	srv := newNotificationsTestServer(t, st)

	rec := httptest.NewRecorder()
	req := withNotificationTestUser(httptest.NewRequest(http.MethodPost, "/api/notifications/notif_1/read", nil), "alice")
	srv.markNotificationRead(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark read: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}

	list, err := st.ListNotificationsForUser(ctx, "alice")
	if err != nil {
		t.Fatalf("list after read: %v", err)
	}
	if len(list) != 1 || !list[0].Read {
		t.Fatalf("expected notification to be marked read: %+v", list)
	}
}

func TestCreateNotificationBroadcastsSSEEvent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newNotificationsTestServer(t, st)

	ch, cancel := srv.hub.Subscribe("")
	defer cancel()

	id, err := srv.CreateNotification(ctx, "Title", "Body")
	if err != nil {
		t.Fatalf("create notification: %v", err)
	}
	if id == "" {
		t.Fatalf("expected non-empty notification id")
	}

	select {
	case event := <-ch:
		if event.Type != "notification.created" {
			t.Fatalf("expected notification.created event, got %q", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected notification.created event to be published")
	}

	list, err := st.ListNotificationsForUser(ctx, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("expected the created notification to be persisted: %+v", list)
	}
}
