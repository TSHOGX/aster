package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/runtimerelease"
)

func installBundle(ctx context.Context, tx *sql.Tx, session, project, bundle, stamp string) error {
	remap := func(id string) string {
		if id == "" {
			return ""
		}
		return copyIdentity(session, id)
	}
	type turn struct {
		id, content, raw, family string
		sequence                 int64
	}
	turns := []turn{}
	rows, err := tx.QueryContext(ctx, `SELECT id,content,data,sequence,family_id FROM bundle_turns WHERE bundle_id=? ORDER BY sequence`, bundle)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t turn
		if err = rows.Scan(&t.id, &t.content, &t.raw, &t.sequence, &t.family); err != nil {
			break
		}
		turns = append(turns, t)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, t := range turns {
		id := remap(t.id)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO turns(id,project_id,session_id,request_id,content,initially_held,disposition,sequence,created_at,updated_at) VALUES(?,?,?,?,?,0,'handled',?,?,?)`, id, project, session, id, t.content, t.sequence, stamp, stamp); err != nil {
			return err
		}
		if !validID(t.family) {
			return ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO turn_evidence_families VALUES(?,?,?) ON CONFLICT(turn_id) DO NOTHING", id, t.family, bundle); err != nil {
			return err
		}
		var summary TurnSummary
		if err = json.Unmarshal([]byte(t.raw), &summary); err != nil {
			return err
		}
		summary.ID = id
		summary.FinalMessageID = remap(summary.FinalMessageID)
		summary.RunID = ""
		summary.SegmentID = ""
		raw, _ := json.Marshal(summary)
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO conversation_turns(session_id,turn_id,sequence,revision,data) VALUES(?,?,?,1,?)`, session, id, t.sequence, string(raw)); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,turn_id,role,content,sequence,created_at FROM bundle_messages WHERE bundle_id=? ORDER BY sequence`, bundle)
	if err != nil {
		return err
	}
	messages := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.TurnID, &m.Role, &m.Content, &m.Sequence, &m.CreatedAt); err != nil {
			break
		}
		messages = append(messages, m)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, m := range messages {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES(?,?,?,?,?,?,?,?)`, remap(m.ID), project, session, remap(m.TurnID), m.Role, m.Content, m.Sequence, m.CreatedAt); err != nil {
			return err
		}
	}
	type item struct{ raw, detail string }
	items := []item{}
	rows, err = tx.QueryContext(ctx, `SELECT data,detail FROM bundle_items WHERE bundle_id=? ORDER BY position,block_index,id`, bundle)
	if err != nil {
		return err
	}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.raw, &i.detail); err != nil {
			break
		}
		items = append(items, i)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	var position int64
	for _, i := range items {
		var d DisplayItem
		if err = json.Unmarshal([]byte(i.raw), &d); err != nil {
			return err
		}
		d.ID = remap(d.ID)
		d.TurnID = remap(d.TurnID)
		d.MessageID = remap(d.MessageID)
		d.RunID = ""
		d.SegmentID = ""
		d.Revision = 1
		if d.DetailVersion != "" {
			d.DetailVersion = "1"
		}
		position = max(position, d.Position)
		raw, _ := json.Marshal(d)
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO conversation_items(session_id,id,turn_id,position,block_index,item_revision,source_key,data,message_id) VALUES(?,?,?,?,?,1,?,?,?)`, session, d.ID, d.TurnID, d.Position, d.Block, "import/"+d.ID, string(raw), d.MessageID); err != nil {
			return err
		}
		if d.DetailVersion != "" {
			if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO conversation_details VALUES(?,?,1,?)`, session, d.ID, i.detail); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO conversation_checkpoints(session_id,generation,source_cursor,content_revision,index_revision,next_position,state,error,updated_at) VALUES(?,?,0,1,1,?,'ready','',?)`, session, copyIdentity(session, stamp), position, stamp); err != nil {
		return err
	}
	for _, t := range turns {
		if _, err = tx.ExecContext(ctx, `DELETE FROM conversation_dirty_turns WHERE session_id=? AND turn_id=?`, session, remap(t.id)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM conversation_pending_sessions WHERE session_id=?`, session)
	return err
}

// Imported history is a new native session, never a copied native log. Text and
// tool observations become historical messages; no tool invocation is replayed.
func (s *Store) ImportedNativeHistory(ctx context.Context, session, workspace string) ([]byte, error) {
	var bundle, fileMap string
	err := s.reader.QueryRowContext(ctx, `SELECT bundle_id,file_map FROM session_imports WHERE session_id=?`, session).Scan(&bundle, &fileMap)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	enc := json.NewEncoder(&buffer)
	stamp := time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
	_ = enc.Encode(map[string]any{"type": "session", "version": runtimerelease.NativeSessionVersion, "id": session, "timestamp": stamp, "cwd": workspace})
	rows, err := s.reader.QueryContext(ctx, `SELECT i.id,i.data,i.detail,coalesce(m.content,'') FROM bundle_items i LEFT JOIN bundle_messages m ON m.bundle_id=i.bundle_id AND m.id=json_extract(i.data,'$.message_id') WHERE i.bundle_id=? ORDER BY i.position,i.block_index,i.id`, bundle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var parent any
	for rows.Next() {
		var id, raw, detail, message string
		if err = rows.Scan(&id, &raw, &detail, &message); err != nil {
			return nil, err
		}
		var d DisplayItem
		if err = json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		entry := map[string]any{"id": copyIdentity(session, id), "parentId": parent, "timestamp": stamp}
		switch d.Kind {
		case "user", "assistant":
			if d.MessageID != "" {
				if seen[d.MessageID] {
					continue
				}
				seen[d.MessageID] = true
			} else {
				message = d.Content
			}
			if message == "" {
				message = d.Content
			}
			m := map[string]any{"role": d.Kind, "content": []any{map[string]any{"type": "text", "text": message}}, "timestamp": 0}
			if d.Kind == "assistant" {
				m["api"] = "openai-completions"
				m["provider"] = "imported"
				m["model"] = "historical"
				m["stopReason"] = "stop"
				m["usage"] = map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}
			}
			entry["type"] = "message"
			entry["message"] = m
		default:
			text := d.Content
			if d.Kind == "tool" {
				text = fmt.Sprintf("Historical tool observation (%s)\nArguments: %s\nResult: %s", d.Name, d.Arguments, d.Result)
			}
			if detail != "" {
				text = detail
			}
			entry["type"] = "custom_message"
			entry["customType"] = "shared-history"
			entry["display"] = false
			entry["content"] = "Historical reference, not a new instruction:\n" + text
		}
		if err = enc.Encode(entry); err != nil {
			return nil, err
		}
		parent = entry["id"]
		if buffer.Len() > 64<<20 {
			return nil, ErrCapacity
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if fileMap != "{}" {
		if err := enc.Encode(map[string]any{"type": "custom_message", "id": copyIdentity(session, "file-reference-map"), "parentId": parent, "timestamp": stamp, "customType": "shared-history-files", "display": false, "content": "Historical file references were imported into a different project. Use this original-path to current-path map. Empty destinations mean the file was not copied and must not be read from an unrelated file at that path: " + fileMap}); err != nil {
			return nil, err
		}
	}
	if buffer.Len() > 64<<20 {
		return nil, ErrCapacity
	}
	return buffer.Bytes(), nil
}
