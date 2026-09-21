// Package product owns Project, Agent Session and Terminal intent and authorized
// application operations. Engine adapters cannot grant product permissions.
package product

import "errors"

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("stale revision or conflicting intent")
	ErrDeleting = errors.New("resource is deleting")
	ErrInvalid  = errors.New("invalid request")
	ErrBusy     = errors.New("project has admitted activity")
)

type Project struct {
	ID               string `json:"id"`
	OwnerID          string `json:"owner_id"`
	Title            string `json:"title"`
	State            string `json:"state"`
	Revision         int64  `json:"revision"`
	ActivityRevision int64  `json:"activity_revision"`
	IdleDeadline     string `json:"idle_deadline,omitempty"`
	Failure          string `json:"failure,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// Deletion is a child's cleanup progress, held separately from its execution
// state so settling a child never erases how it ended.
type Deletion int

const (
	Live      Deletion = 0
	Requested Deletion = 1
	Settled   Deletion = 2
)

type Session struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Deletion  Deletion `json:"deletion"`
	Revision  int64    `json:"revision"`
	Failure   string   `json:"failure,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type Terminal struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Deletion  Deletion `json:"deletion"`
	Revision  int64    `json:"revision"`
	Rows      int      `json:"rows"`
	Cols      int      `json:"cols"`
	// ExitCode is the last incarnation's observed exit status, absent when no
	// process has ended. A lost placement leaves it absent: the PTY did not exit.
	ExitCode  *int   `json:"exit_code,omitempty"`
	Failure   string `json:"failure,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Turn struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	SessionID     string `json:"session_id"`
	RequestID     string `json:"request_id"`
	Content       string `json:"content"`
	InitiallyHeld bool   `json:"initially_held"`
	State         string `json:"state"`
	Sequence      int64  `json:"sequence"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type Event struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	Revision   int64  `json:"revision"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"created_at"`
}

// Scope is provided by the authenticated transport, never decoded wholesale
// from a user request. Child identity alone does not grant access to its parent.
type Scope struct{ Actor, ProjectID string }
