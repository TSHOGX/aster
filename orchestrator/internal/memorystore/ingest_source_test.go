package memorystore

import (
	"context"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// testSource is the same adapter internal/controlsource provides in production.
// It is duplicated here rather than imported because controlsource imports this
// package, so an in-package test importing it back would be a cycle. That the
// duplicate is ~20 lines of field copying is itself the point: the boundary is
// narrow enough that a consumer can implement it trivially.
type testSource struct {
	control *store.Store
}

func newTestSource(control *store.Store) *testSource {
	return &testSource{control: control}
}

var _ SessionSource = (*testSource)(nil)

func (s *testSource) SessionTranscript(ctx context.Context, sessionID string) (SessionTranscript, error) {
	row, err := s.control.SessionTranscript(ctx, sessionID)
	if err != nil {
		return SessionTranscript{}, err
	}
	messages := make([]TranscriptMessage, 0, len(row.Messages))
	for _, m := range row.Messages {
		messages = append(messages, TranscriptMessage{
			ID:        m.ID,
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
			Trace:     m.Trace,
		})
	}
	return SessionTranscript{
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

func (s *testSource) ListPiSessionsForCapture(ctx context.Context, since time.Time) ([]PiSessionForCapture, error) {
	rows, err := s.control.ListPiSessionsForCapture(ctx, since)
	if err != nil {
		return nil, err
	}
	out := make([]PiSessionForCapture, 0, len(rows))
	for _, r := range rows {
		out = append(out, PiSessionForCapture{
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
