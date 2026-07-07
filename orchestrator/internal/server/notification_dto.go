package server

import (
	"time"

	"harness-platform/orchestrator/internal/store"
)

type apiNotification struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
}

func publicNotification(n store.NotificationWithReadState) apiNotification {
	return apiNotification{
		ID:        n.ID,
		Title:     n.Title,
		Body:      n.Body,
		CreatedAt: n.CreatedAt,
		Read:      n.Read,
	}
}

func publicNotifications(notifications []store.NotificationWithReadState) []apiNotification {
	items := make([]apiNotification, 0, len(notifications))
	for _, n := range notifications {
		items = append(items, publicNotification(n))
	}
	return items
}
