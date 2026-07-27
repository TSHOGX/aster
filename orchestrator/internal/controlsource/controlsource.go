// Package controlsource adapts the control-plane store to the memory pipeline's
// ingest boundary. It is the ONLY place in the tree that imports both
// internal/store and internal/memorystore.
//
// It exists so neither of those packages has to know about the other: memorystore
// declares what control data it needs (memorystore.SessionSource), internal/store
// exposes plain control-plane reads, and this package — a pure translation layer
// with no state and no SQL of its own — joins them. That keeps the dependency
// unidirectional (control -> memory), keeps the memory half free of any handle it
// could write control data through, and means a change to either schema shows up
// here as a compile error rather than as silent coupling.
package controlsource

import (
	"context"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/store"
)

// Source implements memorystore.SessionSource over the control-plane store.
type Source struct {
	control *store.Store
}

// New wraps a control store as an ingest source. A nil control store yields nil,
// so a caller with no control database simply has no projection available.
func New(control *store.Store) *Source {
	if control == nil {
		return nil
	}
	return &Source{control: control}
}

// compile-time proof that the adapter satisfies the consumer-side interface.
var _ memorystore.SessionSource = (*Source)(nil)

// SessionTranscript translates one control-plane transcript row into the
// boundary's value type.
func (s *Source) SessionTranscript(ctx context.Context, sessionID string) (memorystore.SessionTranscript, error) {
	row, err := s.control.SessionTranscript(ctx, sessionID)
	if err != nil {
		return memorystore.SessionTranscript{}, err
	}
	messages := make([]memorystore.TranscriptMessage, 0, len(row.Messages))
	for _, m := range row.Messages {
		messages = append(messages, memorystore.TranscriptMessage{
			ID:        m.ID,
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
			Trace:     m.Trace,
		})
	}
	return memorystore.SessionTranscript{
		SessionID: row.SessionID,
		Status:    row.Status,
		UserID:    row.UserID,
		Agent:     row.Agent,
		CreatedAt: row.CreatedAt,
		EndedAt:   row.EndedAt,
		TurnCount: row.TurnCount,
		Messages:  messages,
	}, nil
}

// ListPiSessionsForCapture translates the control-plane capture enumeration.
func (s *Source) ListPiSessionsForCapture(ctx context.Context, since time.Time) ([]memorystore.PiSessionForCapture, error) {
	rows, err := s.control.ListPiSessionsForCapture(ctx, since)
	if err != nil {
		return nil, err
	}
	out := make([]memorystore.PiSessionForCapture, 0, len(rows))
	for _, r := range rows {
		out = append(out, memorystore.PiSessionForCapture{
			SessionID:              r.SessionID,
			UserID:                 r.UserID,
			Status:                 r.Status,
			AgentHomeHostPath:      r.AgentHomeHostPath,
			SelectedSessionRelpath: r.SelectedSessionRelpath,
			NativeSessionID:        r.NativeSessionID,
			EndedAt:                r.EndedAt,
		})
	}
	return out, nil
}
