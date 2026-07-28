package store

import (
	"context"
	"database/sql"
	"time"
)

// Notification is a message shown in the workbench. An empty UserID is a
// broadcast (release notes, system status); a non-empty one is directed at that
// user alone.
type Notification struct {
	ID string
	// UserID is the audience. "" means everyone.
	UserID    string
	Title     string
	Body      string
	CreatedAt time.Time
}

// notificationAudiencePredicate restricts a notification read to what userID may
// see: broadcasts plus their own directed rows.
//
// It is a shared constant rather than inline SQL because it MUST appear in every
// read of the table. There are two, and if only one carried it the unread badge
// and the list it opens would disagree -- the failure mode is a user seeing a
// count they cannot account for, or a notification that never clears.
const notificationAudiencePredicate = `(n.user_id IS NULL OR n.user_id = ?)`

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
INSERT INTO notifications (id, user_id, title, body, created_at) VALUES (?, ?, ?, ?, ?)`,
		n.ID, nullableString(n.UserID), n.Title, n.Body, formatTime(n.CreatedAt))
	return err
}

// ListNotificationsForUser returns the notifications userID may see -- broadcasts
// plus rows directed at them -- newest first, annotated with read state.
func (s *Store) ListNotificationsForUser(ctx context.Context, userID string) ([]NotificationWithReadState, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT n.id, n.user_id, n.title, n.body, n.created_at, nr.user_id IS NOT NULL
FROM notifications n
LEFT JOIN notification_reads nr ON nr.notification_id = n.id AND nr.user_id = ?
WHERE `+notificationAudiencePredicate+`
ORDER BY n.created_at DESC`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications := []NotificationWithReadState{}
	for rows.Next() {
		var (
			n         NotificationWithReadState
			target    sql.NullString
			createdAt string
		)
		if err := rows.Scan(&n.ID, &target, &n.Title, &n.Body, &createdAt, &n.Read); err != nil {
			return nil, err
		}
		if target.Valid {
			n.UserID = target.String
		}
		n.CreatedAt = parseTime(createdAt)
		notifications = append(notifications, n)
	}
	return notifications, rows.Err()
}

// CountUnreadNotifications returns how many of the notifications userID may see
// they have not yet read.
//
// The audience predicate here must match ListNotificationsForUser exactly: this
// count labels the badge that opens that list, so a divergence shows up as a
// badge the user cannot clear.
func (s *Store) CountUnreadNotifications(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notifications n
WHERE `+notificationAudiencePredicate+`
  AND NOT EXISTS (
    SELECT 1 FROM notification_reads nr WHERE nr.notification_id = n.id AND nr.user_id = ?
  )`, userID, userID).Scan(&count)
	return count, err
}

// MarkNotificationRead records that userID has read notificationID. Idempotent.
func (s *Store) MarkNotificationRead(ctx context.Context, notificationID, userID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO notification_reads (notification_id, user_id, read_at)
VALUES (?, ?, ?)`, notificationID, userID, formatTime(now))
	return err
}
