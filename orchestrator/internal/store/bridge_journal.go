package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// BridgeRecord is durable producer input. Its lifetime is independent of the
// public event log; deleting SSE history must never erase unprojected output.
type BridgeRecord struct {
	ID                      int64
	SessionID, GenerationID string
	TurnID                  int64
	Attempt                 int
	Sequence                int64
	Type, Stream            string
	Payload                 json.RawMessage
	CreatedAt               time.Time
}

type BridgeProjection struct {
	LastRecordID int64
	State        json.RawMessage
}

type ProjectedMessage struct {
	Content, StopReason string
	Trace               json.RawMessage
}

var ErrProjectionConflict = errors.New("bridge projection checkpoint changed")

func bridgeRecordKey(p AppendEventParams) string {
	if p.Type == "emit_output" && p.OutputSequence != nil {
		return fmt.Sprintf("output:%d", *p.OutputSequence)
	}
	if p.Type == "ack_turn_completed" {
		return "completion"
	}
	return ""
}

// appendBridgeRecordTx runs in the same transaction as the accepted source
// event. The retained digest also detects contradictory replay after SSE GC.
func appendBridgeRecordTx(ctx context.Context, tx *sql.Tx, p AppendEventParams, payload []byte) (bool, error) {
	key := bridgeRecordKey(p)
	if key == "" || p.TurnID == nil {
		return false, nil
	}
	var attempt int
	if err := tx.QueryRowContext(ctx, `SELECT attempt FROM turns WHERE id=? AND session_id=? AND generation_id=?`, *p.TurnID, p.SessionID, p.GenerationID).Scan(&attempt); err != nil {
		return false, err
	}
	if p.Type == "ack_turn_completed" {
		// Session projection fields describe the first commit, not the source
		// completion. They may differ on a same-command retry.
		var source map[string]json.RawMessage
		if err := json.Unmarshal(payload, &source); err != nil {
			return false, err
		}
		for _, field := range []string{"session_marked_idle", "session_terminal", "session_status", "session_updated_at", "session_last_activity_at", "active_generation_id"} {
			delete(source, field)
		}
		payload, err := json.Marshal(source)
		if err != nil {
			return false, err
		}
		return appendBridgeRecordPayloadTx(ctx, tx, p, attempt, key, payload)
	}
	return appendBridgeRecordPayloadTx(ctx, tx, p, attempt, key, payload)
}

func appendBridgeRecordPayloadTx(ctx context.Context, tx *sql.Tx, p AppendEventParams, attempt int, key string, payload []byte) (bool, error) {
	digest := sha256.Sum256(append([]byte(p.Type+"\x00"+p.Stream+"\x00"), payload...))
	hash := hex.EncodeToString(digest[:])
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT digest FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND attempt=? AND record_key=?`, p.GenerationID, *p.TurnID, attempt, key).Scan(&existing)
	if err == nil {
		if existing != hash {
			return false, fmt.Errorf("%w for turn %d generation %s key %s", errDuplicateOutputSequenceMismatch, *p.TurnID, p.GenerationID, key)
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_ingress(session_id,generation_id,turn_id,attempt,record_key,output_sequence,type,stream,payload,digest,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.SessionID, p.GenerationID, *p.TurnID, attempt, key, nullableInt64Ptr(p.OutputSequence), p.Type, p.Stream, string(payload), hash, formatEventTime(p.Now))
	return false, err
}

// PendingBridgeRecords is bounded and ordered across streams. A caller must
// finish each record before advancing its stream's checkpoint.
func (s *Store) PendingBridgeRecords(ctx context.Context, limit int) ([]BridgeRecord, error) {
	if limit <= 0 || limit > 1024 {
		limit = 128
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.session_id,i.generation_id,i.turn_id,i.attempt,coalesce(i.output_sequence,0),i.type,i.stream,i.payload,i.created_at
 FROM bridge_ingress i LEFT JOIN bridge_projections p ON p.generation_id=i.generation_id AND p.turn_id=i.turn_id AND p.attempt=i.attempt
 WHERE i.id>coalesce(p.last_record_id,0) ORDER BY i.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []BridgeRecord
	for rows.Next() {
		var r BridgeRecord
		var stamp, payload string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.GenerationID, &r.TurnID, &r.Attempt, &r.Sequence, &r.Type, &r.Stream, &payload, &stamp); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload)
		r.CreatedAt, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (s *Store) LoadBridgeProjection(ctx context.Context, r BridgeRecord) (BridgeProjection, error) {
	var p BridgeProjection
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT last_record_id,state FROM bridge_projections WHERE generation_id=? AND turn_id=? AND attempt=?`, r.GenerationID, r.TurnID, r.Attempt).Scan(&p.LastRecordID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	p.State = json.RawMessage(state)
	return p, err
}

// CommitBridgeProjection atomically writes all messages, their traces and the
// parser checkpoint. A retry cannot allocate another product message identity.
func (s *Store) CommitBridgeProjection(ctx context.Context, r BridgeRecord, previous int64, state json.RawMessage, messages []ProjectedMessage) ([]Message, error) {
	if !json.Valid(state) {
		return nil, fmt.Errorf("invalid bridge projection state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO bridge_projections(generation_id,turn_id,attempt,last_record_id,state) VALUES(?,?,?,0,'{}')`, r.GenerationID, r.TurnID, r.Attempt)
	if err != nil {
		return nil, err
	}
	var current, next int64
	err = tx.QueryRowContext(ctx, `SELECT last_record_id FROM bridge_projections WHERE generation_id=? AND turn_id=? AND attempt=?`, r.GenerationID, r.TurnID, r.Attempt).Scan(&current)
	if err != nil {
		return nil, err
	}
	if current != previous {
		return nil, ErrProjectionConflict
	}
	err = tx.QueryRowContext(ctx, `SELECT coalesce(min(id),0) FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND attempt=? AND session_id=? AND id>?`, r.GenerationID, r.TurnID, r.Attempt, r.SessionID, current).Scan(&next)
	if err != nil {
		return nil, err
	}
	if next != r.ID || next == 0 {
		return nil, ErrProjectionConflict
	}
	var result []Message
	for ordinal, m := range messages {
		if len(m.Trace) > 0 && !json.Valid(m.Trace) {
			return nil, fmt.Errorf("invalid projected trace")
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO messages(session_id,turn_id,role,content,created_at,stop_reason) VALUES(?,?,'assistant',?,?,?)`, r.SessionID, r.TurnID, m.Content, formatTime(r.CreatedAt), m.StopReason)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO bridge_projected_messages(record_id,ordinal,message_id) VALUES(?,?,?)`, r.ID, ordinal, id)
		if err != nil {
			return nil, err
		}
		if len(m.Trace) > 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO message_traces(message_id,session_id,nodes,created_at) VALUES(?,?,?,?)`, id, r.SessionID, string(m.Trace), formatTime(r.CreatedAt))
			if err != nil {
				return nil, err
			}
		}
		result = append(result, Message{ID: id, SessionID: r.SessionID, Role: "assistant", Content: m.Content, StopReason: m.StopReason, CreatedAt: r.CreatedAt, Trace: m.Trace})
	}
	_, err = tx.ExecContext(ctx, `UPDATE bridge_projections SET last_record_id=?,state=? WHERE generation_id=? AND turn_id=? AND attempt=?`, r.ID, string(state), r.GenerationID, r.TurnID, r.Attempt)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
