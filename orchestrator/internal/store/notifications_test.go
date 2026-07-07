package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openNotificationsTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "notifications.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCreateAndListNotifications(t *testing.T) {
	st := openNotificationsTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.CreateNotification(ctx, Notification{ID: "notif_1", Title: "First", Body: "body 1", CreatedAt: now}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if err := st.CreateNotification(ctx, Notification{ID: "notif_2", Title: "Second", Body: "body 2", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	list, err := st.ListNotificationsForUser(ctx, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(list))
	}
	// Newest first.
	if list[0].ID != "notif_2" || list[1].ID != "notif_1" {
		t.Fatalf("expected newest-first order, got %+v", list)
	}
	for _, n := range list {
		if n.Read {
			t.Fatalf("expected unread notification, got read: %+v", n)
		}
	}

	unread, err := st.CountUnreadNotifications(ctx, "alice")
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 2 {
		t.Fatalf("expected 2 unread, got %d", unread)
	}

	if err := st.MarkNotificationRead(ctx, "notif_1", "alice", now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark read: %v", err)
	}

	list, err = st.ListNotificationsForUser(ctx, "alice")
	if err != nil {
		t.Fatalf("list after read: %v", err)
	}
	var gotRead, gotUnread bool
	for _, n := range list {
		switch n.ID {
		case "notif_1":
			gotRead = n.Read
		case "notif_2":
			gotUnread = !n.Read
		}
	}
	if !gotRead {
		t.Fatalf("expected notif_1 to be read: %+v", list)
	}
	if !gotUnread {
		t.Fatalf("expected notif_2 to remain unread: %+v", list)
	}

	unread, err = st.CountUnreadNotifications(ctx, "alice")
	if err != nil {
		t.Fatalf("count unread after read: %v", err)
	}
	if unread != 1 {
		t.Fatalf("expected 1 unread after marking one read, got %d", unread)
	}
}

func TestMarkNotificationReadIsPerUser(t *testing.T) {
	st := openNotificationsTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.CreateNotification(ctx, Notification{ID: "notif_1", Title: "Title", Body: "Body", CreatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.MarkNotificationRead(ctx, "notif_1", "alice", now); err != nil {
		t.Fatalf("mark read for alice: %v", err)
	}

	aliceList, err := st.ListNotificationsForUser(ctx, "alice")
	if err != nil {
		t.Fatalf("list for alice: %v", err)
	}
	if len(aliceList) != 1 || !aliceList[0].Read {
		t.Fatalf("expected alice to have read the notification: %+v", aliceList)
	}

	bobList, err := st.ListNotificationsForUser(ctx, "bob")
	if err != nil {
		t.Fatalf("list for bob: %v", err)
	}
	if len(bobList) != 1 || bobList[0].Read {
		t.Fatalf("expected bob's read state to be independent of alice's: %+v", bobList)
	}
}

func TestMarkNotificationReadIdempotent(t *testing.T) {
	st := openNotificationsTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.CreateNotification(ctx, Notification{ID: "notif_1", Title: "Title", Body: "Body", CreatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.MarkNotificationRead(ctx, "notif_1", "alice", now); err != nil {
		t.Fatalf("mark read (first): %v", err)
	}
	if err := st.MarkNotificationRead(ctx, "notif_1", "alice", now.Add(time.Second)); err != nil {
		t.Fatalf("mark read (second, idempotent): %v", err)
	}

	unread, err := st.CountUnreadNotifications(ctx, "alice")
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 0 {
		t.Fatalf("expected 0 unread, got %d", unread)
	}
}
