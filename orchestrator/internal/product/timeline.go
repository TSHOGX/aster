package product

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// TimelineItem is an ordered display projection, not a second execution journal.
// Keys and positions originate at the first event, so completion only updates content.
type TimelineItem struct {
	ID        string `json:"id"`
	Cursor    int64  `json:"cursor"`
	Order     int    `json:"order"`
	TurnID    string `json:"turn_id"`
	Kind      string `json:"kind"`
	Content   string `json:"content,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	State     string `json:"state,omitempty"`
}

func clipped(value string, limit int) string {
	r := []rune(value)
	if len(r) > limit {
		return string(r[:limit]) + "\n…"
	}
	return value
}

// Extract text before bounding it so long tool output does not become invalid
// JSON full of escaped newlines in the display projection.
func toolResultPreview(raw json.RawMessage) string {
	var result struct {
		Content []struct {
			Type string
			Text *string
		}
	}
	if json.Unmarshal(raw, &result) == nil && len(result.Content) > 0 {
		text := make([]string, 0, len(result.Content))
		for _, block := range result.Content {
			if block.Type != "text" || block.Text == nil {
				return clipped(string(raw), 8192)
			}
			text = append(text, *block.Text)
		}
		return clipped(strings.Join(text, "\n"), 8192)
	}
	return clipped(string(raw), 8192)
}

func sessionTimeline(ctx context.Context, q queryer, session string, messages []Message, turns []Turn, latest bool) ([]TimelineItem, bool, error) {
	ids := map[string]bool{}
	for _, m := range messages {
		if m.TurnID != "" {
			ids[m.TurnID] = true
		}
	}
	if latest {
		for _, t := range turns {
			if t.State == "starting" || t.State == "running" || t.State == "stopping" {
				ids[t.ID] = true
			}
		}
	}
	out := []TimelineItem{}
	if len(ids) == 0 {
		return out, false, nil
	}
	args := []any{session}
	marks := []string{}
	for id := range ids {
		args = append(args, id)
		marks = append(marks, "?")
	}
	// Pi message_update carries the current native message. Collapse snapshots in
	// SQL rather than transferring all text deltas on each poll. The start group
	// survives updates, message_end, journal replay and browser reload. Window
	// sorts carry metadata only: streaming snapshots grow with every token, so
	// sorting whole payloads (and their first-value copies) multiplies memory.
	// Fetch just the selected first/latest payloads after pagination.
	sql := `WITH metadata AS MATERIALIZED (
 SELECT r.rowid AS cursor,r.incarnation_id,r.sequence,r.turn_id,r.kind,
 json_extract(r.payload,'$.type','$.message.role','$.toolCallId') AS meta
 FROM agent_records r JOIN agent_incarnations i ON i.id=r.incarnation_id
 WHERE i.session_id=? AND r.turn_id IN (` + strings.Join(marks, ",") + `)
 ), events AS (
 SELECT cursor,incarnation_id,sequence,turn_id,kind,
 json_extract(meta,'$[0]') AS type,json_extract(meta,'$[1]') AS role,json_extract(meta,'$[2]') AS tool_call_id,
 sum(CASE WHEN json_extract(meta,'$[0]')='message_start' AND json_extract(meta,'$[1]')='assistant' THEN 1 ELSE 0 END) OVER (PARTITION BY incarnation_id,turn_id ORDER BY cursor) AS message_group
 FROM metadata
 ), keyed AS (
 SELECT *, CASE WHEN type IN ('message_start','message_update','message_end') THEN 'message/'||incarnation_id||'/'||turn_id||'/'||message_group
 WHEN type IN ('tool_execution_start','tool_execution_update','tool_execution_end') THEN 'tool/'||turn_id||'/'||coalesce(tool_call_id,cursor)
 ELSE 'user/'||turn_id END AS item_key
 FROM events WHERE (type IN ('message_start','message_update','message_end') AND role='assistant')
 OR type IN ('tool_execution_start','tool_execution_update','tool_execution_end') OR kind='command.applied'
 ), ranked AS (
 SELECT *, min(cursor) OVER(PARTITION BY item_key) AS position,
 row_number() OVER(PARTITION BY item_key ORDER BY cursor DESC) AS rank
 FROM keyed
 ), selected AS (
 SELECT * FROM ranked WHERE rank=1 ORDER BY position DESC LIMIT 501
 ) SELECT s.item_key,s.position,s.cursor,s.incarnation_id,s.sequence,s.turn_id,s.type,s.kind,r.payload,f.payload
 FROM selected s JOIN agent_records r ON r.rowid=s.cursor JOIN agent_records f ON f.rowid=s.position
 ORDER BY s.position DESC`
	rows, err := q.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	byID := map[string]Message{}
	userByTurn := map[string]Message{}
	for _, m := range messages {
		byID[m.ID] = m
		if m.Role == "user" {
			userByTurn[m.TurnID] = m
		}
	}
	emitted := map[string]bool{}
	truncated := false
	bytes := 0
	count := 0
	for rows.Next() {
		var key, incarnation, turn, kind string
		var eventType *string
		var pos, cursor, sequence int64
		var raw, first []byte
		if err = rows.Scan(&key, &pos, &cursor, &incarnation, &sequence, &turn, &eventType, &kind, &raw, &first); err != nil {
			return nil, false, err
		}
		count++
		if count > 500 {
			truncated = true
			break
		}
		var event struct {
			Type    string
			Message struct {
				Role    string
				Content []struct {
					Type, Text, Thinking, ID, Name string
					Arguments                      json.RawMessage
				}
			}
			ToolName      string
			Args          json.RawMessage
			Result        json.RawMessage
			PartialResult json.RawMessage
			IsError       bool
		}
		if err = json.Unmarshal(raw, &event); err != nil {
			return nil, false, err
		}
		items := []TimelineItem{}
		if kind == "command.applied" {
			if m, ok := userByTurn[turn]; ok {
				items = append(items, TimelineItem{ID: m.ID, MessageID: m.ID, Cursor: pos, TurnID: turn, Kind: "user", Content: m.Content})
				emitted[m.ID] = true
			}
		}
		if strings.HasPrefix(key, "message/") {
			messageID := ""
			if event.Type == "message_end" {
				messageID = fmt.Sprintf("%s/%d", incarnation, sequence)
			}
			// Do not duplicate messages outside the current history page.
			if messageID != "" {
				if _, ok := byID[messageID]; !ok {
					hasText := false
					for _, b := range event.Message.Content {
						if b.Type == "text" && b.Text != "" {
							hasText = true
						}
					}
					if hasText {
						continue
					}
				}
				emitted[messageID] = true
			}
			for index, block := range event.Message.Content {
				if block.Type == "toolCall" && block.ID != "" {
					items = append(items, TimelineItem{
						ID: "tool/" + turn + "/" + block.ID, Cursor: pos, Order: index, TurnID: turn,
						Kind: "tool", Name: block.Name, Arguments: clipped(string(block.Arguments), 8192), State: "pending",
					})
					continue
				}
				k, text := "", ""
				switch block.Type {
				case "text":
					k, text = "assistant", block.Text
				case "thinking":
					k, text = "thinking", block.Thinking
				}
				if k == "" || text == "" {
					continue
				}
				state := "completed"
				if event.Type != "message_end" {
					state = "streaming"
				}
				items = append(items, TimelineItem{ID: fmt.Sprintf("%s/%d", key, index), Cursor: pos, Order: index, TurnID: turn, Kind: k, Content: clipped(text, 64000), State: state, MessageID: messageID})
			}
		}
		if strings.HasPrefix(key, "tool/") {
			var start struct {
				ToolName string
				Args     json.RawMessage
			}
			_ = json.Unmarshal(first, &start)
			name := event.ToolName
			if name == "" {
				name = start.ToolName
			}
			result := event.Result
			if len(result) == 0 {
				result = event.PartialResult
			}
			state := "running"
			if event.Type == "tool_execution_end" {
				state = "completed"
				if event.IsError {
					state = "failed"
				}
			}
			items = append(items, TimelineItem{ID: key, Cursor: pos, TurnID: turn, Kind: "tool", Name: name, Arguments: clipped(string(start.Args), 8192), Result: toolResultPreview(result), State: state})
		}
		n := 0
		for _, item := range items {
			n += len(item.Content) + len(item.Arguments) + len(item.Result)
		}
		if bytes+n > 2<<20 && len(out) > 0 {
			truncated = true
			break
		}
		bytes += n
		out = append(out, items...)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}

	// A call is visible while the model is still preparing its arguments. Merge
	// execution snapshots into that original row, preserving identity and order
	// even when several calls start or finish in a different order.
	executions := map[string]TimelineItem{}
	requested := map[string]bool{}
	for _, item := range out {
		if item.Kind != "tool" {
			continue
		}
		if item.State == "pending" {
			requested[item.ID] = true
		} else {
			executions[item.ID] = item
		}
	}
	merged := make([]TimelineItem, 0, len(out))
	for _, item := range out {
		if item.Kind == "tool" {
			if item.State != "pending" && requested[item.ID] {
				continue
			}
			if execution, ok := executions[item.ID]; ok && item.State == "pending" {
				execution.Cursor = item.Cursor
				execution.Order = item.Order
				if execution.Arguments == "" {
					execution.Arguments = item.Arguments
				}
				if execution.Name == "" {
					execution.Name = item.Name
				}
				item = execution
			}
		}
		merged = append(merged, item)
	}
	out = merged
	// Imported/legacy messages with no native events remain readable. Place only
	// with a known same-turn anchor; never invent positions for absent tool data.
	for _, m := range messages {
		if emitted[m.ID] {
			continue
		}
		pos := int64(0)
		for _, item := range out {
			if item.TurnID == m.TurnID && (pos == 0 || item.Cursor < pos) {
				pos = item.Cursor
			}
		}
		if m.Role == "user" {
			pos--
		} else if pos != 0 {
			pos++
		}
		out = append(out, TimelineItem{ID: m.ID, Cursor: pos, TurnID: m.TurnID, Kind: m.Role, Content: clipped(m.Content, 64000), MessageID: m.ID})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cursor != out[j].Cursor {
			return out[i].Cursor < out[j].Cursor
		}
		return out[i].Order < out[j].Order
	})
	return out, truncated, nil
}
