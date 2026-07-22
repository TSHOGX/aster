package server

import (
	"time"

	"harness-platform/orchestrator/internal/store"
)

type apiSession struct {
	ID                    string     `json:"id"`
	UserID                string     `json:"user_id"`
	Status                string     `json:"status"`
	Mode                  string     `json:"mode"`
	ModeLabel             string     `json:"mode_label"`
	RestoreMS             *int64     `json:"restore_ms,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	EndedAt               *time.Time `json:"ended_at,omitempty"`
	LastActivityAt        *time.Time `json:"last_activity_at,omitempty"`
	AutoCheckpointEnabled bool       `json:"auto_checkpoint_enabled"`
	FailureReason         string     `json:"failure_reason,omitempty"`
	ErrorClass            string     `json:"error_class,omitempty"`
	Title                 string     `json:"title"`
}

func publicSession(session store.Session) apiSession {
	mode := session.Mode
	if mode == "" {
		mode = store.ModeForDriver(session.Agent)
	}
	return apiSession{
		ID:                    session.ID,
		UserID:                session.UserID,
		Status:                session.Status,
		Mode:                  mode,
		ModeLabel:             modeLabel(mode),
		RestoreMS:             session.RestoreMS,
		CreatedAt:             session.CreatedAt,
		UpdatedAt:             session.UpdatedAt,
		ExpiresAt:             session.ExpiresAt,
		EndedAt:               session.EndedAt,
		LastActivityAt:        session.LastActivityAt,
		AutoCheckpointEnabled: session.AutoCheckpointEnabled,
		FailureReason:         session.FailureReason,
		ErrorClass:            session.ErrorClass,
		Title:                 session.Title,
	}
}

// apiSearchHit is the wire form of a store.SearchHit. Field names are part of
// the /api/search contract consumed by the frontend and must not change without
// coordinating that change.
type apiSearchHit struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Mode      string `json:"mode"`
	MessageID *int64 `json:"message_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Snippet   string `json:"snippet"`
	CreatedAt string `json:"created_at"`
}

func publicSearchHit(hit store.SearchHit) apiSearchHit {
	return apiSearchHit{
		Kind:      hit.Kind,
		SessionID: hit.SessionID,
		Title:     hit.Title,
		Status:    hit.Status,
		Mode:      hit.Mode,
		MessageID: hit.MessageID,
		Role:      hit.Role,
		Snippet:   hit.Snippet,
		CreatedAt: hit.CreatedAt,
	}
}

func publicSearchHits(hits []store.SearchHit) []apiSearchHit {
	items := make([]apiSearchHit, 0, len(hits))
	for _, hit := range hits {
		items = append(items, publicSearchHit(hit))
	}
	return items
}

func modeLabel(mode string) string {
	switch mode {
	case "shell":
		return "Shell"
	default:
		return "Agent"
	}
}

func publicSessions(sessions []store.Session) []apiSession {
	items := make([]apiSession, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, publicSession(session))
	}
	return items
}
