package store

// This file holds the durable event-log append path, split out of
// controlplane.go (which was ~1491 lines). It follows the interface grouping:
// these are the writes behind the event concern -- AppendEvent, its
// transactional form, and the idempotency/ordering assertions that guard a
// duplicate or out-of-order output event. The turn lease state machine
// (claim/resume/ack/complete) stays in controlplane.go. Declarations were moved
// verbatim; no SQL changed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var errOutputEventTurnNotRunning = errors.New("output event turn already terminal")

func IsDuplicateOutputSequenceMismatch(err error) bool {
	return errors.Is(err, errDuplicateOutputSequenceMismatch)
}

func IsOutputEventTurnNotRunning(err error) bool {
	return errors.Is(err, errOutputEventTurnNotRunning)
}

func (s *Store) AppendEvent(ctx context.Context, p AppendEventParams) (int64, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.Type == "" {
		return 0, fmt.Errorf("event type is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	eventID, err := appendOneEventTx(ctx, tx, p)
	if err != nil {
		return 0, err
	}
	return eventID, tx.Commit()
}

func appendEventTx(ctx context.Context, tx *sql.Tx, p AppendEventParams) (int64, error) {
	payload, err := json.Marshal(p.Payload)
	if err != nil {
		return 0, err
	}
	duplicate, err := appendBridgeRecordTx(ctx, tx, p, payload)
	if err != nil {
		return 0, err
	}
	if duplicate {
		return 0, nil
	}
	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO events (
  session_id, turn_id, generation_id, output_sequence, dedupe_key,
  proxy_request_id, stream, severity, type, payload, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableString(p.SessionID), nullableInt64Ptr(p.TurnID), nullableString(p.GenerationID),
		nullableInt64Ptr(p.OutputSequence), nullableString(p.DedupeKey), nullableString(p.ProxyRequestID),
		nullableString(p.Stream), nullableString(p.Severity), p.Type, string(payload), formatEventTime(p.Now))
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		if err := assertDuplicateEventMatches(ctx, tx, p, string(payload)); err != nil {
			return 0, err
		}
		return 0, nil
	}
	return res.LastInsertId()
}

func assertDuplicateEventMatches(ctx context.Context, tx *sql.Tx, p AppendEventParams, payload string) error {
	if p.OutputSequence == nil || p.TurnID == nil {
		return nil
	}
	var existingType, existingPayload string
	var existingStream sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT type, stream, payload
FROM events
WHERE turn_id = ?
  AND generation_id = ?
  AND output_sequence = ?`,
		*p.TurnID, p.GenerationID, *p.OutputSequence).Scan(&existingType, &existingStream, &existingPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	stream := ""
	if existingStream.Valid {
		stream = existingStream.String
	}
	if existingType == p.Type && stream == p.Stream && existingPayload == payload {
		return nil
	}
	return fmt.Errorf("%w for turn %d generation %s sequence %d", errDuplicateOutputSequenceMismatch, *p.TurnID, p.GenerationID, *p.OutputSequence)
}

func assertOutputEventTurnTx(ctx context.Context, tx *sql.Tx, p AppendEventParams) error {
	if p.TurnID == nil {
		return fmt.Errorf("output event requires turn id")
	}
	var turnStatus string
	err := tx.QueryRowContext(ctx, `
SELECT t.status
FROM turns t
JOIN runtime_generations g ON g.generation_id = t.generation_id
JOIN sessions s ON s.id = t.session_id
WHERE t.id = ?
  AND t.session_id = ?
  AND t.generation_id = ?
  AND t.lease_owner = ?
  AND t.lease_expires_at > ?
  AND g.generation_id = ?
  AND g.session_id = ?
  AND g.lease_owner = ?
  AND g.lease_expires_at > ?
  AND s.active_generation_id = ?`,
		*p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now),
		p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.GenerationID).Scan(&turnStatus)
	if errors.Is(err, sql.ErrNoRows) {
		// Lease/generation checks failed but we cannot see the turn: transient
		// (lease expired, wrong generation, etc). Retry might succeed.
		return fmt.Errorf("output event turn CAS failed")
	}
	if err != nil {
		return err
	}
	if turnStatus != "running" {
		// Turn exists but is terminal: permanent. The generation sent output for
		// a turn the host already completed/failed. Discard the event.
		return fmt.Errorf("%w: turn %d status %s", errOutputEventTurnNotRunning, *p.TurnID, turnStatus)
	}
	return nil
}

// AppendEventsBatchResult reports the outcome of one event in a batch, in the
// order the events were submitted.
type AppendEventsBatchResult struct {
	// EventID is the durable id, or 0 when the row was a duplicate that the
	// INSERT OR IGNORE dropped (identical replay).
	EventID int64
	// Err is the per-event failure, when this event alone was rejected. A
	// sequence-mismatch error here is the same value AppendEvent would have
	// returned, so IsDuplicateOutputSequenceMismatch still classifies it.
	Err error
}

// AppendEventsBatch appends several events in ONE transaction, returning a result
// per event in submission order.
//
// Why this exists: bridge output was one transaction per event, and on a single
// writer connection that made a chatty turn's stdout the dominant source of
// write-lock contention. Everything else that needs the writer — turn lease
// heartbeats above all — queues behind it, and a heartbeat that lands late lapses
// a turn's lease, which used to fail the turn's model calls outright. Batching is
// therefore not a throughput nicety; it removes the contention that produced
// spurious "model unavailable" failures.
//
// Semantics are deliberately identical to calling AppendEvent per event:
//   - each event is validated with the same assertOutputEventTurnTx CAS,
//   - duplicates remain idempotent (INSERT OR IGNORE + assertDuplicateEventMatches),
//   - order is preserved.
//
// One event's failure does NOT roll back its neighbours. A sequence mismatch is
// attributed to the single event that caused it (the caller retires the
// generation over that one envelope), so folding a batch would either lose good
// output or misattribute the fault to whichever envelope happened to share the
// batch. Each event therefore gets a SAVEPOINT: on failure only that event is
// rolled back and the rest of the batch still commits.
func (s *Store) AppendEventsBatch(ctx context.Context, events []AppendEventParams) ([]AppendEventsBatchResult, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	results := make([]AppendEventsBatchResult, len(events))
	for i := range events {
		p := events[i]
		if p.Now.IsZero() {
			p.Now = time.Now().UTC()
		}
		if p.Type == "" {
			results[i].Err = fmt.Errorf("event type is required")
			continue
		}
		// A named savepoint per event: a rejected event must not discard the
		// events already appended in this transaction.
		name := fmt.Sprintf("batch_event_%d", i)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
			return nil, err
		}
		eventID, err := appendOneEventTx(ctx, tx, p)
		if err != nil {
			if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO "+name); rbErr != nil {
				return nil, rbErr
			}
			// Release even after a rollback-to, or the savepoint stays on the stack.
			if _, relErr := tx.ExecContext(ctx, "RELEASE "+name); relErr != nil {
				return nil, relErr
			}
			results[i].Err = err
			continue
		}
		if _, err := tx.ExecContext(ctx, "RELEASE "+name); err != nil {
			return nil, err
		}
		results[i].EventID = eventID
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

// appendOneEventTx is AppendEvent's body without the transaction, so the single
// and batched paths cannot drift in what they validate.
func appendOneEventTx(ctx context.Context, tx *sql.Tx, p AppendEventParams) (int64, error) {
	if p.OutputSequence != nil {
		if err := assertOutputEventTurnTx(ctx, tx, p); err != nil {
			return 0, err
		}
	}
	return appendEventTx(ctx, tx, p)
}
