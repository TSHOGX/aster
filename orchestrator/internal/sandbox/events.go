package sandbox

import "context"

type Event struct {
	Sequence                   int64  `json:"sequence"`
	ID                         string `json:"id"`
	SandboxID                  string `json:"sandbox_id"`
	Kind, State                string
	Epoch                      uint64
	Address, Cause, OccurredAt string
}
type EventPage struct {
	StoreID          string
	Events           []Event
	Earliest, Latest int64
}
type EventWindowExpired struct {
	Page  EventPage
	Cause error
}

func (e *EventWindowExpired) Error() string { return "engine event cursor is outside retained window" }
func (e *EventWindowExpired) Unwrap() error { return e.Cause }

type EventSource interface {
	Events(context.Context, int64, int) (EventPage, error)
}
