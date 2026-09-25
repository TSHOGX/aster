package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const displayInlineBytes = 64 << 10
const displayBatchBytes = 1 << 20
const displayBatchRecords = 128

// DisplayItem is a complete replaceable row. All external 64-bit positions are
// decimal strings; native source rowids never appear in cursors or identity.
type DisplayItem struct {
	ID            string `json:"id"`
	RunID         string `json:"run_id,omitempty"`
	TurnID        string `json:"turn_id"`
	SegmentID     string `json:"segment_id,omitempty"`
	Position      int64  `json:"position,string"`
	Block         int    `json:"block"`
	Revision      int64  `json:"item_revision,string"`
	Kind          string `json:"kind"`
	Content       string `json:"content,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	Name          string `json:"name,omitempty"`
	Arguments     string `json:"arguments,omitempty"`
	Result        string `json:"result,omitempty"`
	State         string `json:"state,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	DetailVersion string `json:"detail_version,omitempty"`
}
type displayCheckpoint struct {
	Generation                        string
	Source, Revision, Index, Position int64
	State, Error                      string
}
type displaySource struct {
	ID, Sequence            int64
	Command                 string
	Incarnation, Turn, Kind string
	Payload                 []byte
}
type nativeDisplayEvent struct {
	Type                        string
	ToolCallID                  string `json:"toolCallId"`
	ToolName                    string
	Args, Result, PartialResult json.RawMessage
	IsError                     bool
	Message                     struct {
		Role    string
		Content []struct {
			Type, Text, Thinking, ID, Name string
			Arguments                      json.RawMessage
		}
	}
}

func checkpoint(ctx context.Context, q queryer, session string) (displayCheckpoint, error) {
	var p displayCheckpoint
	err := q.QueryRowContext(ctx, `SELECT generation,source_cursor,content_revision,index_revision,next_position,state,error FROM conversation_checkpoints WHERE session_id=?`, session).Scan(&p.Generation, &p.Source, &p.Revision, &p.Index, &p.Position, &p.State, &p.Error)
	return p, err
}

// ProjectConversation commits one bounded batch. It never wakes a Project,
// issues a command, acknowledges a journal or infers an execution outcome.
func (s *Store) ProjectConversation(ctx context.Context, session string) error {
	if pressure, err := s.displayPressure(ctx, session); err != nil {
		return err
	} else if pressure {
		if err = s.PruneConversation(ctx, session); err != nil {
			return err
		}
		return ErrPending
	}
	var exists string
	if err := s.reader.QueryRowContext(ctx, `SELECT id FROM sessions WHERE id=? AND deletion_intent<2`, session).Scan(&exists); err != nil {
		return missing(err)
	}
	before, err := checkpoint(ctx, s.reader, session)
	if errors.Is(err, sql.ErrNoRows) {
		before = displayCheckpoint{Generation: uuid.NewString(), State: "building"}
	} else if err != nil {
		return err
	}
	sources := []displaySource{}
	rows, err := s.reader.QueryContext(ctx, `SELECT c.source_id,c.incarnation_id,c.sequence,r.turn_id,r.kind,r.payload,r.command_id FROM conversation_sources c JOIN agent_records r ON r.incarnation_id=c.incarnation_id AND r.sequence=c.sequence WHERE c.session_id=? AND c.source_id>? ORDER BY c.source_id LIMIT ?`, session, before.Source, displayBatchRecords)
	if err != nil {
		return err
	}
	bytes := 0
	for rows.Next() {
		var r displaySource
		if err = rows.Scan(&r.ID, &r.Incarnation, &r.Sequence, &r.Turn, &r.Kind, &r.Payload, &r.Command); err != nil {
			break
		}
		if bytes+len(r.Payload) > displayBatchBytes && len(sources) > 0 {
			break
		}
		sources = append(sources, r)
		bytes += len(r.Payload)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	err = s.writeClass(ctx, backgroundWrite, "", int64(bytes+len(sources)*256), func(tx *sql.Tx) error {
		if e := tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE id=? AND deletion_intent<2`, session).Scan(&exists); e != nil {
			return missing(e)
		}
		current, e := checkpoint(ctx, tx, session)
		if e == nil && current != before {
			return ErrConflict
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_checkpoints(session_id,generation,updated_at) VALUES(?,?,?)`, session, before.Generation, s.timestamp()); e != nil {
			return e
		}
		reducer := displayReducer{ctx: ctx, tx: tx, session: session, p: before, changes: map[string]*DisplayItem{}, sourceKeys: map[string]string{}, batchStart: before.Source}
		for _, source := range sources {
			if e = reducer.consume(source); e != nil {
				return e
			}
			reducer.p.Source = source.ID
			if len(reducer.changes) >= 512 {
				break
			}
		}
		var rawPending bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_sources WHERE session_id=? AND source_id>?)`, session, reducer.p.Source).Scan(&rawPending); e != nil {
			return e
		}
		if e = reducer.summaries(); e != nil {
			return e
		}
		if e = reducer.flush(); e != nil {
			return e
		}
		var turnPending bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_dirty_turns WHERE session_id=?)`, session).Scan(&turnPending); e != nil {
			return e
		}
		state := "ready"
		if rawPending || turnPending {
			state = "catching_up"
			if before.Revision == 0 {
				state = "building"
			}
		}
		if state == "ready" {
			if _, e = tx.ExecContext(ctx, `DELETE FROM conversation_pending_sessions WHERE session_id=?`, session); e != nil {
				return e
			}
		}
		if reducer.p.Revision != before.Revision || reducer.p.Index != before.Index || state != before.State {
			if _, e = tx.ExecContext(ctx, `INSERT INTO events(project_id,session_id,revision,kind,created_at) SELECT project_id,id,?,'conversation.view',? FROM sessions WHERE id=?`, max(int64(1), reducer.p.Revision), s.timestamp(), session); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE conversation_checkpoints SET source_cursor=?,content_revision=?,index_revision=?,next_position=?,state=?,error='',updated_at=? WHERE session_id=?`, reducer.p.Source, reducer.p.Revision, reducer.p.Index, reducer.p.Position, state, s.timestamp(), session)
		return e
	})
	if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrPending) && !errors.Is(err, ErrCapacity) && ctx.Err() == nil {
		// A failure marker is separate from the rolled-back batch. It exposes lag
		// without allowing the checkpoint to skip malformed evidence.
		_ = s.writeClass(ctx, backgroundWrite, "", 0, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `INSERT INTO conversation_checkpoints(session_id,generation,state,error,updated_at) SELECT id,?,'failed','display projection failed',? FROM sessions WHERE id=? AND deletion_intent<2 ON CONFLICT(session_id) DO UPDATE SET state='failed',error=excluded.error`, before.Generation, s.timestamp(), session)
			return e
		})
	}
	return err
}

type displayReducer struct {
	batchStart int64
	ctx        context.Context
	tx         *sql.Tx
	session    string
	p          displayCheckpoint
	changes    map[string]*DisplayItem
	sourceKeys map[string]string
}

func (d *displayReducer) get(id string) (*DisplayItem, error) {
	if v, ok := d.changes[id]; ok {
		return v, nil
	}
	var data string
	err := d.tx.QueryRowContext(d.ctx, `SELECT coalesce((SELECT content FROM conversation_details WHERE session_id=i.session_id AND item_id=i.id AND item_revision=i.item_revision),i.data) FROM conversation_items i WHERE i.session_id=? AND i.id=?`, d.session, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var item DisplayItem
	err = json.Unmarshal([]byte(data), &item)
	return &item, err
}
func (d *displayReducer) put(item DisplayItem, source string) error {
	old, err := d.get(item.ID)
	if err != nil {
		return err
	}
	if old != nil {
		item.TurnID, item.RunID, item.SegmentID = old.TurnID, old.RunID, old.SegmentID
		if item.Position < 0 {
			item.Position = old.Position
		}
		item.Block = old.Block
		item.Revision = old.Revision
		if item == *old {
			return nil
		}
	} else if item.Position < 0 {
		d.p.Position++
		item.Position = d.p.Position
	}
	d.changes[item.ID] = &item
	d.sourceKeys[item.ID] = source
	return nil
}
func (d *displayReducer) group(r displaySource, start bool) (string, int64, error) {
	var sequence, position int64
	var ended bool
	err := d.tx.QueryRowContext(d.ctx, `SELECT start_sequence,position,ended FROM conversation_message_groups WHERE session_id=? AND incarnation_id=? AND turn_id=?`, d.session, r.Incarnation, r.Turn).Scan(&sequence, &position, &ended)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	if start || ended || errors.Is(err, sql.ErrNoRows) {
		sequence = r.Sequence
		d.p.Position++
		position = d.p.Position
		_, err = d.tx.ExecContext(d.ctx, `INSERT INTO conversation_message_groups(session_id,incarnation_id,turn_id,start_sequence,position,ended) VALUES(?,?,?,?,?,0) ON CONFLICT(session_id,incarnation_id,turn_id) DO UPDATE SET start_sequence=excluded.start_sequence,position=excluded.position,ended=0`, d.session, r.Incarnation, r.Turn, sequence, position)
	}
	return fmt.Sprintf("message/%s/%s/%d", r.Incarnation, r.Turn, sequence), position, err
}
func (d *displayReducer) consume(r displaySource) error {
	if r.Turn == "" {
		return nil
	}
	if r.Kind == "unknown" {
		// Process loss is an execution fact. It closes live presentation clocks
		// without inventing a wall-clock end or elapsed duration.
		_, err := d.tx.ExecContext(d.ctx, `UPDATE conversation_segments SET state='unknown' WHERE session_id=? AND run_id=? AND state IN ('running','settling')`, d.session, r.Command)
		return err
	}
	if r.Kind == "command.completed" {
		var e struct{ Outcome string }
		if err := json.Unmarshal(r.Payload, &e); err != nil {
			return err
		}
		if e.Outcome == "" {
			e.Outcome = "completed"
		}
		_, err := d.tx.ExecContext(d.ctx, `UPDATE conversation_segments SET state=? WHERE session_id=? AND run_id=? AND state<>'continued'`, e.Outcome, d.session, r.Command)
		return err
	}
	if r.Kind != "native" {
		return nil
	}
	var header struct{ Type string }
	if err := json.Unmarshal(r.Payload, &header); err != nil {
		return err
	}
	switch header.Type {
	case "input_consumed":
		var event struct {
			InputToken string `json:"input_token"`
			RunID      string `json:"run_id"`
		}
		if err := json.Unmarshal(r.Payload, &event); err != nil || event.InputToken == "" || event.RunID == "" {
			return ErrInvalid
		}
		var prompt, content string
		if err := d.tx.QueryRowContext(d.ctx, `SELECT prompt_id,t.content FROM input_deliveries d JOIN turns t ON t.id=d.prompt_id WHERE input_token=? AND d.session_id=? AND d.run_id=? AND d.state='consumed'`, event.InputToken, d.session, event.RunID).Scan(&prompt, &content); err != nil {
			return err
		}
		message := prompt + "/user"
		if old, err := d.get(message); err != nil {
			return err
		} else if old != nil {
			return nil
		}
		var evidence struct {
			OccurredAt string `json:"occurred_at"`
			Elapsed    *int64 `json:"run_elapsed_ms"`
		}
		if err := json.Unmarshal(r.Payload, &evidence); err != nil {
			return err
		}
		segment, owner, _, err := d.currentSegment(event.RunID)
		if err != nil {
			return err
		}
		var worked bool
		if segment != "" {
			if err = d.tx.QueryRowContext(d.ctx, `SELECT has_work FROM conversation_segments WHERE session_id=? AND id=?`, d.session, segment).Scan(&worked); err != nil {
				return err
			}
		}
		if segment == "" || worked {
			if segment != "" {
				if err = d.closeSegment(event.RunID, "continued", evidence.OccurredAt, evidence.Elapsed); err != nil {
					return err
				}
			}
			segment = fmt.Sprintf("segment/%s/%s", event.RunID, prompt)
			owner = prompt
			if _, err = d.tx.ExecContext(d.ctx, `INSERT INTO conversation_segments(session_id,id,run_id,prompt_id,position,started_at,start_elapsed_ms) VALUES(?,?,?,?,?,?,?)`, d.session, segment, event.RunID, prompt, r.ID, evidence.OccurredAt, evidence.Elapsed); err != nil {
				return err
			}
		}
		if _, err = d.tx.ExecContext(d.ctx, `INSERT INTO conversation_segment_inputs VALUES(?,?,?)`, d.session, prompt, segment); err != nil {
			return err
		}
		_ = owner
		return d.put(DisplayItem{ID: message, TurnID: prompt, RunID: event.RunID, SegmentID: segment, Position: -1, Kind: "user", Content: content, MessageID: message, State: "consumed"}, "consumed/"+event.InputToken)
	case "inputs_reconciled":
		var event struct {
			RunID      string `json:"run_id"`
			OccurredAt string `json:"occurred_at"`
			Elapsed    *int64 `json:"run_elapsed_ms"`
		}
		if err := json.Unmarshal(r.Payload, &event); err != nil {
			return err
		}
		return d.closeSegment(event.RunID, "settling", event.OccurredAt, event.Elapsed)
	case "message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_update", "tool_execution_end":
	default:
		return nil
	}
	var e nativeDisplayEvent
	if err := json.Unmarshal(r.Payload, &e); err != nil {
		return err
	}
	switch e.Type {
	case "message_start", "message_update", "message_end":
		if e.Message.Role != "assistant" {
			return nil
		}
		if len(e.Message.Content) > 512 {
			return ErrCapacity
		}
		key, pos, err := d.group(r, e.Type == "message_start")
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for index, block := range e.Message.Content {
			segment, prompt, _, err := d.currentSegment(r.Command)
			if err != nil {
				return err
			}
			if prompt == "" {
				prompt = r.Turn
			}
			item := DisplayItem{ID: fmt.Sprintf("%s/%d", key, index), TurnID: prompt, RunID: r.Command, SegmentID: segment, Position: pos, Block: index, State: "streaming"}
			if e.Type == "message_end" {
				item.State = "completed"
				item.MessageID = fmt.Sprintf("%s/%d", r.Incarnation, r.Sequence)
			}
			switch block.Type {
			case "text":
				item.Kind = "assistant"
				item.Content = block.Text
			case "thinking":
				item.Kind = "thinking"
				item.Content = block.Thinking
			case "toolCall":
				if block.ID == "" {
					continue
				}
				item.ID = "tool/" + r.Incarnation + "/" + r.Turn + "/" + block.ID
				item.Kind = "tool"
				item.Name = block.Name
				item.Arguments = string(block.Arguments)
				item.State = "pending"
				item.MessageID = ""
				old, err := d.get(item.ID)
				if err != nil {
					return err
				}
				if old != nil && old.State != "pending" {
					keep[item.ID] = true
					continue
				}
			default:
				continue // Unknown source blocks remain in retained raw evidence.
			}
			sideSegment := segment
			if prior, lookupErr := d.get(item.ID); lookupErr != nil {
				return lookupErr
			} else if prior != nil {
				sideSegment = prior.SegmentID
			}
			if sideSegment != "" {
				final := ""
				if e.Type == "message_end" && item.Kind == "assistant" {
					final = item.MessageID
				}
				if _, err = d.tx.ExecContext(d.ctx, `UPDATE conversation_segments SET has_work=1,final_message_id=CASE WHEN ?<>'' THEN ? ELSE final_message_id END WHERE session_id=? AND id=?`, final, final, d.session, sideSegment); err != nil {
					return err
				}
			}
			keep[item.ID] = true
			if err = d.put(item, key); err != nil {
				return err
			}
		}
		// Snapshot replacements can remove a block. Record explicit tombstones.
		rows, err := d.tx.QueryContext(d.ctx, `SELECT id FROM conversation_items WHERE session_id=? AND source_key=?`, d.session, key)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for id, source := range d.sourceKeys {
			if source == key {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if !keep[id] {
				old, err := d.get(id)
				if err != nil {
					return err
				}
				if old != nil && (old.Kind != "tool" || old.State == "pending") {
					d.changes[id] = nil
				}
			}
		}
		if e.Type == "message_end" {
			if _, err = d.tx.ExecContext(d.ctx, `UPDATE conversation_message_groups SET ended=1 WHERE session_id=? AND incarnation_id=? AND turn_id=?`, d.session, r.Incarnation, r.Turn); err != nil {
				return err
			}
		}
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		id := e.ToolCallID
		if id == "" {
			id = fmt.Sprint(r.Sequence)
		}
		id = "tool/" + r.Incarnation + "/" + r.Turn + "/" + id
		old, err := d.get(id)
		if err != nil {
			return err
		}
		segment, prompt, _, err := d.currentSegment(r.Command)
		if err != nil {
			return err
		}
		item := DisplayItem{ID: id, TurnID: prompt, RunID: r.Command, SegmentID: segment, Position: -1, Kind: "tool", State: "running"}
		if old != nil {
			item = *old
			item.State = "running"
			segment, prompt = old.SegmentID, old.TurnID
		} else if segment != "" {
			if _, err = d.tx.ExecContext(d.ctx, `UPDATE conversation_segments SET has_work=1 WHERE session_id=? AND id=?`, d.session, segment); err != nil {
				return err
			}
		}
		if e.ToolName != "" {
			item.Name = e.ToolName
		}
		if len(e.Args) > 0 {
			item.Arguments = string(e.Args)
		}
		if len(e.Result) > 0 {
			item.Result = displayResult(e.Result)
		} else if len(e.PartialResult) > 0 {
			item.Result = displayResult(e.PartialResult)
		}
		if e.Type == "tool_execution_end" {
			item.State = "completed"
			if e.IsError {
				item.State = "failed"
			}
		}
		return d.put(item, "execution/"+id)
	}
	return nil
}
func displayResult(raw json.RawMessage) string {
	var value struct {
		Content []struct {
			Type string
			Text *string
		}
	}
	if json.Unmarshal(raw, &value) == nil && len(value.Content) > 0 {
		parts := []string{}
		for _, b := range value.Content {
			if b.Type != "text" || b.Text == nil {
				return string(raw)
			}
			parts = append(parts, *b.Text)
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}
func (d *displayReducer) summaries() error {
	// Segment updates can affect several input anchors, including older prompts.
	if _, err := d.tx.ExecContext(d.ctx, `INSERT OR IGNORE INTO conversation_dirty_turns SELECT session_id,prompt_id FROM conversation_segment_inputs WHERE session_id=? AND segment_id IN (SELECT id FROM conversation_segments WHERE session_id=? AND run_id IN (SELECT command_id FROM agent_records r JOIN conversation_sources c ON c.incarnation_id=r.incarnation_id AND c.sequence=r.sequence WHERE c.session_id=? AND c.source_id>? AND c.source_id<=?))`, d.session, d.session, d.session, d.batchStart, d.p.Source); err != nil {
		return err
	}
	rows, err := d.tx.QueryContext(d.ctx, `SELECT t.id,t.sequence,
 CASE t.disposition WHEN 'pending' THEN CASE t.queue_policy WHEN 'held' THEN 'held' ELSE 'queued' END
 WHEN 'claimed' THEN coalesce((SELECT CASE r.state WHEN 'settled' THEN r.outcome ELSE r.state END FROM execution_runs r WHERE r.prompt_id=t.id ORDER BY r.started_at DESC,r.id DESC LIMIT 1),'claimed')
 WHEN 'canceled' THEN 'canceled'
 ELSE coalesce((SELECT CASE r.state WHEN 'settled' THEN r.outcome ELSE r.state END FROM input_deliveries x JOIN execution_runs r ON r.id=x.run_id WHERE x.prompt_id=t.id AND x.state='consumed' ORDER BY x.acceptance_sequence DESC LIMIT 1),'consumed') END,
 coalesce((SELECT min(consumed_at) FROM input_deliveries WHERE prompt_id=t.id AND state='consumed' AND consumed_at<>''),''),t.updated_at,
 coalesce((SELECT m.id FROM messages m WHERE m.session_id=t.session_id AND m.turn_id=t.id AND m.role='assistant' ORDER BY m.sequence DESC LIMIT 1),'')
 FROM conversation_dirty_turns d JOIN turns t ON t.id=d.turn_id AND t.session_id=d.session_id WHERE d.session_id=? ORDER BY t.sequence LIMIT 64`, d.session)
	if err != nil {
		return err
	}
	summaries := []TurnSummary{}
	for rows.Next() {
		var t TurnSummary
		var updated, final string
		if err = rows.Scan(&t.ID, &t.Sequence, &t.State, &t.CreatedAt, &updated, &final); err != nil {
			break
		}
		switch t.State {
		case "completed":
			t.FinalMessageID = final
			t.FinishedAt = updated
		case "failed", "canceled", "unknown":
			t.FinishedAt = updated
		}
		summaries = append(summaries, t)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(summaries) > 0 {
		d.p.Index++
	}
	for _, t := range summaries {
		var segment TurnSummary
		err = d.tx.QueryRowContext(d.ctx, `SELECT s.id,s.run_id,CASE WHEN s.state='continued' THEN 'continued' WHEN coalesce(r.state,'')='unknown' THEN 'unknown' WHEN coalesce(r.state,'')='failed' THEN 'failed' WHEN coalesce(r.state,'')='stopping' THEN 'stopping' ELSE s.state END,s.started_at,s.finished_at,s.elapsed_ms,s.final_message_id FROM conversation_segment_inputs i JOIN conversation_segments s ON s.session_id=i.session_id AND s.id=i.segment_id LEFT JOIN execution_runs r ON r.session_id=s.session_id AND r.id=s.run_id WHERE i.session_id=? AND i.prompt_id=?`, d.session, t.ID).Scan(&segment.SegmentID, &segment.RunID, &segment.State, &segment.CreatedAt, &segment.FinishedAt, &segment.ElapsedMS, &segment.FinalMessageID)
		if err == nil {
			segment.ID = t.ID
			segment.Sequence = t.Sequence
			t = segment
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, _ := json.Marshal(t)
		if _, err = d.tx.ExecContext(d.ctx, `INSERT INTO conversation_turns VALUES(?,?,?,?,?) ON CONFLICT(session_id,turn_id) DO UPDATE SET revision=excluded.revision,data=excluded.data`, d.session, t.ID, t.Sequence, d.p.Index, string(raw)); err != nil {
			return err
		}
		if _, err = d.tx.ExecContext(d.ctx, `DELETE FROM conversation_dirty_turns WHERE session_id=? AND turn_id=?`, d.session, t.ID); err != nil {
			return err
		}
	}
	return nil
}
func (d *displayReducer) flush() error {
	if len(d.changes) == 0 {
		return nil
	}
	d.p.Revision++
	ids := make([]string, 0, len(d.changes))
	for id := range d.changes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for ordinal, id := range ids {
		item := d.changes[id]
		operation := "upsert"
		data := ""
		if item == nil {
			operation = "delete"
			if _, err := d.tx.ExecContext(d.ctx, `DELETE FROM conversation_items WHERE session_id=? AND id=?`, d.session, id); err != nil {
				return err
			}
		} else {
			item.Revision = d.p.Revision
			full, err := json.Marshal(item)
			if err != nil {
				return err
			}
			compact := *item
			if len(full) > displayInlineBytes {
				compact.Truncated = true
				compact.DetailVersion = fmt.Sprint(item.Revision)
				for limit := 8192; ; limit /= 2 {
					compact.Content = clipped(item.Content, limit)
					compact.Arguments = clipped(item.Arguments, limit)
					compact.Result = clipped(item.Result, limit)
					compact.Name = clipped(item.Name, 256)
					raw, _ := json.Marshal(compact)
					if len(raw) <= displayInlineBytes {
						break
					}
					if limit <= 1 {
						return ErrCapacity
					}
				}
				if _, err = d.tx.ExecContext(d.ctx, `INSERT INTO conversation_details VALUES(?,?,?,?)`, d.session, id, item.Revision, string(full)); err != nil {
					return err
				}
			}
			raw, _ := json.Marshal(compact)
			data = string(raw)
			if _, err = d.tx.ExecContext(d.ctx, `INSERT INTO conversation_items(session_id,id,turn_id,position,block_index,item_revision,source_key,data,message_id) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,id) DO UPDATE SET item_revision=excluded.item_revision,source_key=excluded.source_key,data=excluded.data,message_id=excluded.message_id`, d.session, id, item.TurnID, item.Position, item.Block, item.Revision, d.sourceKeys[id], data, item.MessageID); err != nil {
				return err
			}
		}
		if _, err := d.tx.ExecContext(d.ctx, `INSERT INTO conversation_changes(session_id,revision,ordinal,item_id,operation,data) VALUES(?,?,?,?,?,?)`, d.session, d.p.Revision, ordinal, id, operation, data); err != nil {
			return err
		}
	}
	return nil
}

func (d *displayReducer) currentSegment(run string) (segment, prompt, state string, err error) {
	err = d.tx.QueryRowContext(d.ctx, `SELECT id,prompt_id,state FROM conversation_segments WHERE session_id=? AND run_id=? ORDER BY position DESC LIMIT 1`, d.session, run).Scan(&segment, &prompt, &state)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}
func (d *displayReducer) closeSegment(run, state, at string, elapsed *int64) error {
	segment, _, _, err := d.currentSegment(run)
	if err != nil || segment == "" {
		return err
	}
	_, err = d.tx.ExecContext(d.ctx, `UPDATE conversation_segments SET state=?,finished_at=?,elapsed_ms=CASE WHEN start_elapsed_ms IS NOT NULL AND ? IS NOT NULL THEN max(0,?-start_elapsed_ms) ELSE NULL END WHERE session_id=? AND id=? AND finished_at=''`, state, at, elapsed, elapsed, d.session, segment)
	return err
}
