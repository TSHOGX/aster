package server

import (
	"time"

	"harness-platform/orchestrator/internal/store"
)

type apiSessionShare struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Grantee    string    `json:"grantee"`
	Role       string    `json:"role"`
	Visibility string    `json:"visibility"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

func publicSessionShare(share store.SessionShare) apiSessionShare {
	return apiSessionShare{
		ID:         share.ID,
		SessionID:  share.SessionID,
		Grantee:    share.GranteeUserID,
		Role:       share.Role,
		Visibility: share.Visibility,
		CreatedBy:  share.CreatedBy,
		CreatedAt:  share.CreatedAt,
	}
}

func publicSessionShares(shares []store.SessionShare) []apiSessionShare {
	items := make([]apiSessionShare, 0, len(shares))
	for _, share := range shares {
		items = append(items, publicSessionShare(share))
	}
	return items
}

// sharedSession is the session DTO a non-owner sees. It strips the three fields
// that describe the session's owner or its internal failure state: user_id,
// failure_reason, error_class. The same trimming applies to a colleague inside
// the org as to an anonymous link -- "we all work here" is not a reason to leak
// an error class or another account's name.
func sharedSession(session store.Session) apiSession {
	dto := publicSession(session)
	dto.UserID = ""
	dto.FailureReason = ""
	dto.ErrorClass = ""
	return dto
}

// sharedMessage trims one message for a reader at the given visibility.
// It returns ok=false when the message must not be shown at all.
//
// summary keeps user messages and the assistant's turn-final replies, with the
// execution trace stripped. full keeps everything the owner sees except feedback,
// which is per-viewer state the reader has no way to set.
//
// The "turn-final" test is NOT stop_reason == "stop". Shell output goes through
// AddMessage, which writes an empty stop_reason, as does every message that
// predates trace capture; filtering for "stop" would reduce a shell session's
// summary share to the user's own typing with all output deleted. So the rule is
// the inverse: drop the intermediate tool-use steps, keep the rest.
func sharedMessage(message store.Message, visibility string) (store.Message, bool) {
	message.Feedback = ""
	if visibility == store.ShareVisibilityFull {
		return message, true
	}
	if message.Role != "user" && message.StopReason == "toolUse" {
		return store.Message{}, false
	}
	message.Trace = nil
	return message, true
}

func sharedMessages(messages []store.Message, visibility string) []store.Message {
	items := make([]store.Message, 0, len(messages))
	for _, message := range messages {
		trimmed, ok := sharedMessage(message, visibility)
		if !ok {
			continue
		}
		items = append(items, trimmed)
	}
	return items
}

// apiSharedSessionListing is a session on the caller's list annotated with how
// they reach it: "owner" for their own, "reader" plus shared_by for a grant.
type apiSharedSessionListing struct {
	apiSession
	Access   string `json:"access"`
	SharedBy string `json:"shared_by,omitempty"`
	ShareID  string `json:"share_id,omitempty"`
}

func ownedSessionListing(session store.Session) apiSharedSessionListing {
	return apiSharedSessionListing{apiSession: publicSession(session), Access: "owner"}
}

func sharedSessionListing(shared store.SharedSession) apiSharedSessionListing {
	return apiSharedSessionListing{
		apiSession: sharedSession(shared.Session),
		Access:     "reader",
		SharedBy:   shared.SharedBy,
		ShareID:    shared.ShareID,
	}
}
