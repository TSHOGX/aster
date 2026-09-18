package server

import (
	"encoding/json"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// apiSharedLinkSession is the session DTO an anonymous viewer gets. It is built
// field by field rather than by trimming apiSession, so a field added to the
// authed DTO is NOT automatically published here -- adding it to a public page
// has to be a decision somebody makes on purpose.
type apiSharedLinkSession struct {
	MessagePages  bool       `json:"message_pages"`
	ArtifactPages bool       `json:"artifact_pages"`
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Mode          string     `json:"mode"`
	ModeLabel     string     `json:"mode_label"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Visibility    string     `json:"visibility"`
	ExpiresAt     time.Time  `json:"link_expires_at"`
	SharedAt      time.Time  `json:"shared_at"`
	Frozen        bool       `json:"frozen"`
	FrozenAt      *time.Time `json:"frozen_at,omitempty"`
}

func sharedLinkSession(session store.Session, link store.ShareLink) apiSharedLinkSession {
	mode := session.Mode
	if mode == "" {
		mode = store.ModeForDriver(session.Agent)
	}
	dto := apiSharedLinkSession{
		MessagePages: true, ArtifactPages: true,
		ID:         session.ID,
		Title:      session.Title,
		Mode:       mode,
		ModeLabel:  modeLabel(mode),
		Status:     sharedLinkStatus(session.Status),
		CreatedAt:  session.CreatedAt,
		UpdatedAt:  session.UpdatedAt,
		Visibility: link.Visibility,
		ExpiresAt:  link.ExpiresAt,
		SharedAt:   link.CreatedAt,
		Frozen:     link.MaxMessageID != nil,
	}
	if link.MaxMessageID != nil {
		frozenAt := link.CreatedAt
		dto.FrozenAt = &frozenAt
	}
	return dto
}

// sharedLinkStatus collapses the internal session state machine to what a public
// viewer needs: whether the conversation is still going. Publishing
// "checkpointing" or "running_idle" would leak infrastructure detail and mean
// nothing to a reader.
func sharedLinkStatus(status string) string {
	switch status {
	case "failed", "destroyed":
		return "ended"
	case "running_active":
		return "active"
	default:
		return "idle"
	}
}

// apiSharedLinkMessage is the message shape a public viewer gets. Like the
// session DTO it is explicit rather than derived: no feedback, no session id, and
// trace only when the link's visibility says so.
type apiSharedLinkMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	// StopReason is kept so a client can still render a completed answer
	// differently from an interstitial; it carries no internal detail.
	StopReason string `json:"stop_reason,omitempty"`
	// Trace must be json.RawMessage, not []byte: encoding/json base64-encodes a
	// []byte, which would ship the trace as an opaque string the client cannot
	// render.
	Trace json.RawMessage `json:"trace,omitempty"`
}

// sharedLinkMessages applies the link's message window and visibility.
//
// The window (max_message_id) comes first: a link is a snapshot of the
// conversation as it stood when the link was created, so turns that happen later
// never appear in a URL that is already out in the world.
func sharedLinkMessages(messages []store.Message, link store.ShareLink) []apiSharedLinkMessage {
	items := make([]apiSharedLinkMessage, 0, len(messages))
	for _, message := range messages {
		if link.MaxMessageID != nil && message.ID > *link.MaxMessageID {
			continue
		}
		trimmed, ok := sharedMessage(message, link.Visibility)
		if !ok {
			continue
		}
		items = append(items, apiSharedLinkMessage{
			ID:         trimmed.ID,
			Role:       trimmed.Role,
			Content:    trimmed.Content,
			CreatedAt:  trimmed.CreatedAt,
			StopReason: trimmed.StopReason,
			Trace:      trimmed.Trace,
		})
	}
	return items
}

// apiSharedLinkFile is one whitelisted file on a public link. It reports the
// frozen size, never a host path, and never the blob digest (an internal storage
// key, not something a viewer needs).
type apiSharedLinkFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func sharedLinkFiles(files []store.ShareLinkFile) []apiSharedLinkFile {
	items := make([]apiSharedLinkFile, 0, len(files))
	for _, file := range files {
		items = append(items, apiSharedLinkFile{Path: file.Path, Size: file.Size})
	}
	return items
}

// apiShareLink is the owner-facing view of a link. The token is NOT here: it is
// returned exactly once, in the create/rotate response, and only the SHA-256
// hash is stored, so it cannot be echoed back later.
type apiShareLink struct {
	ID             string     `json:"id"`
	SessionID      string     `json:"session_id"`
	Visibility     string     `json:"visibility"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	Expired        bool       `json:"expired"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	AccessCount    int64      `json:"access_count"`
	MaxMessageID   *int64     `json:"max_message_id,omitempty"`
	FileCount      int        `json:"file_count"`
}

func publicShareLink(link store.ShareLink, fileCount int, now time.Time) apiShareLink {
	return apiShareLink{
		ID:             link.ID,
		SessionID:      link.SessionID,
		Visibility:     link.Visibility,
		CreatedBy:      link.CreatedBy,
		CreatedAt:      link.CreatedAt,
		ExpiresAt:      link.ExpiresAt,
		Expired:        !now.Before(link.ExpiresAt),
		LastAccessedAt: link.LastAccessedAt,
		AccessCount:    link.AccessCount,
		MaxMessageID:   link.MaxMessageID,
		FileCount:      fileCount,
	}
}
