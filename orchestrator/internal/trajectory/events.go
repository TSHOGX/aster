package trajectory

import "time"

type TrajectoryEvent struct {
	SessionID string `json:"session_id"`
	Idx       int    `json:"idx"`
	Kind      string `json:"kind"`
	Subtype   string `json:"subtype,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	IsError   bool   `json:"is_error"`
	// NativeID/ParentID carry pi's native intra-session lineage (Tree A): the
	// record's own id and its parentId.
	NativeID  string     `json:"native_id,omitempty"`
	ParentID  string     `json:"parent_id,omitempty"`
	MessageID *int64     `json:"message_id,omitempty"`
	Tokens    *int64     `json:"tokens,omitempty"`
	Text      string     `json:"text"`
	TS        *time.Time `json:"ts,omitempty"`
}
