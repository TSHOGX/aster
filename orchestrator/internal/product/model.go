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

// AdmissionError preserves the machine-readable reason for a rejected control
// while retaining the broad sentinel used by internal transaction tests.
type AdmissionError struct {
	Code  string
	Cause error
}

func (e *AdmissionError) Error() string        { return e.Code + ": " + e.Cause.Error() }
func (e *AdmissionError) Unwrap() error        { return e.Cause }
func Admission(code string, cause error) error { return &AdmissionError{Code: code, Cause: cause} }

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
	ControlRevision int64    `json:"control_revision"`
	ID              string   `json:"id"`
	ProjectID       string   `json:"project_id"`
	Title           string   `json:"title"`
	State           string   `json:"state"`
	Deletion        Deletion `json:"deletion"`
	Revision        int64    `json:"revision"`
	Failure         string   `json:"failure,omitempty"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type Terminal struct {
	Incarnation string   `json:"incarnation,omitempty"`
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Title       string   `json:"title"`
	State       string   `json:"state"`
	Deletion    Deletion `json:"deletion"`
	Revision    int64    `json:"revision"`
	Rows        int      `json:"rows"`
	Cols        int      `json:"cols"`
	// ExitCode is the last incarnation's observed exit status, absent when no
	// process has ended. A lost placement leaves it absent: the PTY did not exit.
	ExitCode  *int   `json:"exit_code,omitempty"`
	Failure   string `json:"failure,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Turn struct {
	QueuePolicy    string `json:"queue_policy"`
	Disposition    string `json:"disposition"`
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	SessionID      string `json:"session_id"`
	RequestID      string `json:"request_id"`
	Content        string `json:"content"`
	InitiallyHeld  bool   `json:"initially_held"`
	PromptRevision int64  `json:"prompt_revision"`
	DeliveryID     string `json:"delivery_id,omitempty"`
	DeliveryState  string `json:"delivery_state,omitempty"`
	DeliveryError  string `json:"delivery_error,omitempty"`
	State          string `json:"state"`
	Sequence       int64  `json:"sequence"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type PromptQueuePage struct {
	Items         []Turn `json:"items"`
	TotalPending  int    `json:"total_pending"`
	QueueRevision int64  `json:"queue_revision"`
	After         int64  `json:"after,omitempty"`
	NextAfter     int64  `json:"next_after,omitempty"`
}

type ExecutionRun struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	SessionID     string `json:"session_id"`
	PromptID      string `json:"prompt_id"`
	IncarnationID string `json:"incarnation_id"`
	Owner         uint64 `json:"owner"`
	State         string `json:"state"`
	Outcome       string `json:"outcome,omitempty"`
	StartedAt     string `json:"started_at"`
	SettledAt     string `json:"settled_at,omitempty"`
	ElapsedMS     int64  `json:"elapsed_ms"`
}

type InputDelivery struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	SessionID          string `json:"session_id"`
	PromptID           string `json:"prompt_id"`
	RequestID          string `json:"request_id"`
	CommandID          string `json:"command_id"`
	RunID              string `json:"run_id"`
	InputToken         string `json:"input_token"`
	Channel            string `json:"channel"`
	State              string `json:"state"`
	AcceptanceSequence int64  `json:"acceptance_sequence"`
	ConsumedSequence   int64  `json:"consumed_sequence,omitempty"`
	AcceptedAt         string `json:"accepted_at"`
	AckAt              string `json:"ack_at,omitempty"`
	ConsumedAt         string `json:"consumed_at,omitempty"`
	RemovedAt          string `json:"removed_at,omitempty"`
	Error              string `json:"error,omitempty"`
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

// ScheduleEvent is a deliberately small owner-scoped invalidation hint. It
// never contains prompts or run contents; clients re-read the authoritative
// schedule and run endpoints after receiving it.
type ScheduleEvent struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id"`
	ScheduleID string `json:"schedule_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	Kind       string `json:"kind"`
}

// Scope is provided by the authenticated transport, never decoded wholesale
// from a user request. Child identity alone does not grant access to its parent.
type Scope struct{ Actor, ProjectID string }
