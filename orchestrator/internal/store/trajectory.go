package store

import (
	"context"
	"fmt"
	"time"
)

// ============================================================================
// CONTROL-side half of the ingest boundary.
//
// Everything else that used to live in this file — the trajectory/memory types,
// the event normalizer, the chunked rebuild, the distilled-memory writes — moved
// to internal/memorystore, which owns a separate database file. What is left here
// is exactly the control-plane read the memory pipeline needs: one session's
// identity, its message list, and its turn count.
//
// This function returns plain control-plane values. It has no knowledge of the
// memory schema or of memorystore's consumer interface; the adapter in
// internal/controlsource is what pairs the two.
// ============================================================================

// SessionTranscriptRow is one session's control-plane state as the memory
// pipeline's projection consumes it: the denormalized session fields it copies
// into its snapshot index, plus the message list and turn count the projection
// and fingerprint are computed from.
type SessionTranscriptRow struct {
	SessionID string
	Status    string
	UserID    string
	Agent     string
	CreatedAt time.Time
	EndedAt   *time.Time
	TurnCount int
	Messages  []Message
}

// SessionTranscript reads everything the memory pipeline needs about one session
// in order to project it into a normalized event stream.
//
// The three reads (session row, the messages+traces+feedback join, turn count)
// are issued separately rather than as one statement, exactly as they were before
// the database split — SnapshotSession already performed them outside any
// transaction. They are NOT wrapped in a transaction here either: doing so would
// hold a read snapshot on the control database for the duration, which is the
// coupling the split exists to remove. A session can therefore grow between the
// three reads; the result is a slightly-stale but self-consistent projection, and
// the fingerprint/normalized_at machinery on the memory side re-processes it on
// the next sweep. See Ingestor.SnapshotSession in internal/memorystore for the
// full torn-read analysis.
func (s *Store) SessionTranscript(ctx context.Context, sessionID string) (SessionTranscriptRow, error) {
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return SessionTranscriptRow{}, fmt.Errorf("session transcript %q: load session: %w", sessionID, err)
	}
	messages, err := s.listMessages(ctx, sessionID, "")
	if err != nil {
		return SessionTranscriptRow{}, fmt.Errorf("session transcript %q: list messages: %w", sessionID, err)
	}

	var turnCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM turns WHERE session_id = ?`, sessionID).Scan(&turnCount); err != nil {
		return SessionTranscriptRow{}, fmt.Errorf("session transcript %q: count turns: %w", sessionID, err)
	}

	return SessionTranscriptRow{
		SessionID: session.ID,
		Status:    session.Status,
		UserID:    session.UserID,
		Agent:     session.Agent,
		CreatedAt: session.CreatedAt,
		EndedAt:   session.EndedAt,
		TurnCount: turnCount,
		Messages:  messages,
	}, nil
}
