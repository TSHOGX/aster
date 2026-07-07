package store

import (
	"context"
	"time"
)

// Notification is a system-wide message (e.g. release notes) broadcast to all users.
type Notification struct {
	ID        string
	Title     string
	Body      string
	CreatedAt time.Time
}

// NotificationWithReadState is a Notification annotated with whether the
// requesting user has read it.
type NotificationWithReadState struct {
	Notification
	Read bool
}

// CreateNotification inserts a new notification row. Internal use only —
// callers are deploy tooling or startup logic, not a public API.
func (s *Store) CreateNotification(ctx context.Context, n Notification) error {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO notifications (id, title, body, created_at) VALUES (?, ?, ?, ?)`,
		n.ID, n.Title, n.Body, formatTime(n.CreatedAt))
	return err
}

// ListNotificationsForUser returns all notifications, newest first, annotated
// with whether userID has read each one.
func (s *Store) ListNotificationsForUser(ctx context.Context, userID string) ([]NotificationWithReadState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT n.id, n.title, n.body, n.created_at, nr.user_id IS NOT NULL
FROM notifications n
LEFT JOIN notification_reads nr ON nr.notification_id = n.id AND nr.user_id = ?
ORDER BY n.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications := []NotificationWithReadState{}
	for rows.Next() {
		var (
			n         NotificationWithReadState
			createdAt string
		)
		if err := rows.Scan(&n.ID, &n.Title, &n.Body, &createdAt, &n.Read); err != nil {
			return nil, err
		}
		n.CreatedAt = parseTime(createdAt)
		notifications = append(notifications, n)
	}
	return notifications, rows.Err()
}

// CountUnreadNotifications returns the number of notifications userID has not
// yet marked read.
func (s *Store) CountUnreadNotifications(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notifications n
WHERE NOT EXISTS (
  SELECT 1 FROM notification_reads nr WHERE nr.notification_id = n.id AND nr.user_id = ?
)`, userID).Scan(&count)
	return count, err
}

// MarkNotificationRead records that userID has read notificationID. Idempotent.
func (s *Store) MarkNotificationRead(ctx context.Context, notificationID, userID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO notification_reads (notification_id, user_id, read_at)
VALUES (?, ?, ?)`, notificationID, userID, formatTime(now))
	return err
}
