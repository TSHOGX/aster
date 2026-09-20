// Package trajectory validates native Pi traces and produces immutable archives.
package trajectory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// PiSessionMeta describes identity and trace metadata read from native JSONL.
type PiSessionMeta struct {
	NativeSessionID string     // pi's own session uuid (session record `id`)
	CWD             string     // working dir the pi session ran in
	Provider        string     // first model_change.provider
	Model           string     // first model_change.modelId
	ParentSession   string     // Tree-B parent (session.parent_session), usually empty
	StartedAt       *time.Time // first parseable record timestamp
	EndedAt         *time.Time // last parseable record timestamp
	TurnCount       int        // count of user|assistant message records
	RecordCount     int        // total records (for the fingerprint)
	Fingerprint     string     // "<recordCount>:<lastEpochSec>" — grows on append
}

// native record + message shapes (only the fields we map).
type piRecord struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	ParentID      string          `json:"parentId"`
	Timestamp     string          `json:"timestamp"` // outer ISO (also present on session head)
	CWD           string          `json:"cwd"`
	ParentSession string          `json:"parent_session"`
	Provider      string          `json:"provider"`
	ModelID       string          `json:"modelId"`
	Message       json.RawMessage `json:"message"`
}

type piMessage struct {
	Role       string          `json:"role"` // user | assistant | toolResult
	Content    json.RawMessage `json:"content"`
	Timestamp  *int64          `json:"timestamp"` // inner epoch MILLIS
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
}

type piContentBlock struct {
	Type       string          `json:"type"` // text | thinking | toolCall
	Text       string          `json:"text"`
	Thinking   string          `json:"thinking"`
	Name       string          `json:"name"`
	ID         string          `json:"id"`
	ToolCallID string          `json:"toolCallId"`
	Arguments  json.RawMessage `json:"arguments"`
}

// ParsePiSession reads a native pi session JSONL stream and returns the ordered
// 7-primitive event stream plus session-level metadata. SessionID on each event
// is left empty for the caller to stamp (the platform session id, not pi's).
//
// Mapping (mirrors sess-cli parsers/pi.ts):
//   - session head           -> meta (id/cwd/parent_session), no event
//   - model_change            -> system, subtype="model_change", text=compact(record)
//   - thinking_level_change   -> system, subtype="thinking_level_change"
//   - message role=user       -> user (text blocks)
//   - message role=assistant  -> text→assistant, thinking→thinking, toolCall→tool_call
//   - message role=toolResult -> tool_result (call_id=toolCallId, is_error=isError)
//
// Each content block becomes its own event but shares the record's native_id and
// parent_id (Tree A). Text is stored full/untruncated. Empty-text events (except
// tool calls, whose text is the args JSON) are dropped. pi records no token
// usage, so no "usage" events are ever emitted.
func ParsePiSession(r io.Reader) ([]TrajectoryEvent, PiSessionMeta, error) {
	var (
		events []TrajectoryEvent
		meta   PiSessionMeta
		idx    int
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024) // pi lines can be large (tool output)

	appendEvent := func(kind, subtype, name, callID, text string, isError bool, rec piRecord, ts *time.Time) {
		if text == "" && kind != "tool_call" {
			return
		}
		events = append(events, TrajectoryEvent{
			Kind:     kind,
			Subtype:  subtype,
			Name:     name,
			CallID:   callID,
			IsError:  isError,
			NativeID: rec.ID,
			ParentID: rec.ParentID,
			Text:     text,
			TS:       ts,
		})
		idx++
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec piRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// Tolerate a malformed line rather than abandoning the whole trace.
			continue
		}
		meta.RecordCount++
		outerTS := parseISO(rec.Timestamp)
		if outerTS != nil {
			if meta.StartedAt == nil {
				meta.StartedAt = outerTS
			}
			meta.EndedAt = outerTS
		}

		switch rec.Type {
		case "session":
			meta.NativeSessionID = rec.ID
			meta.CWD = rec.CWD
			meta.ParentSession = rec.ParentSession
		case "model_change":
			if meta.Provider == "" {
				meta.Provider = rec.Provider
			}
			if meta.Model == "" {
				meta.Model = rec.ModelID
			}
			appendEvent("system", "model_change", "", "", compact(line), false, rec, outerTS)
		case "thinking_level_change":
			appendEvent("system", "thinking_level_change", "", "", compact(line), false, rec, outerTS)
		case "message":
			if len(rec.Message) == 0 {
				continue
			}
			var m piMessage
			if err := json.Unmarshal(rec.Message, &m); err != nil {
				continue
			}
			ts := outerTS
			if m.Timestamp != nil {
				if inner := millisToTime(*m.Timestamp); inner != nil {
					ts = inner
				}
			}
			switch m.Role {
			case "user", "assistant":
				meta.TurnCount++
				kindForText := "user"
				if m.Role == "assistant" {
					kindForText = "assistant"
				}
				for _, blk := range decodeBlocks(m.Content) {
					switch blk.Type {
					case "text", "":
						appendEvent(kindForText, "", "", "", blk.Text, false, rec, ts)
					case "thinking":
						appendEvent("thinking", "", "", "", blk.Thinking, false, rec, ts)
					case "toolCall":
						callID := firstNonEmpty(blk.ToolCallID, blk.ID)
						appendEvent("tool_call", "", blk.Name, callID, compact(string(blk.Arguments)), false, rec, ts)
					default:
						appendEvent("system", blk.Type, "", "", compactBlock(blk), false, rec, ts)
					}
				}
			case "toolResult":
				appendEvent("tool_result", "", m.ToolName, m.ToolCallID, flattenContent(m.Content), m.IsError, rec, ts)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, PiSessionMeta{}, fmt.Errorf("scan pi session: %w", err)
	}

	var lastEpoch int64
	if meta.EndedAt != nil {
		lastEpoch = meta.EndedAt.Unix()
	}
	meta.Fingerprint = strconv.Itoa(meta.RecordCount) + ":" + strconv.FormatInt(lastEpoch, 10)
	return events, meta, nil
}

// decodeBlocks unmarshals a message content field that may be a []block or a
// bare string (some providers emit a plain string for simple text).
func decodeBlocks(raw json.RawMessage) []piContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []piContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []piContentBlock{{Type: "text", Text: s}}
	}
	return nil
}

// flattenContent joins the text of a toolResult content array (or a bare string)
// into one full, untruncated string.
func flattenContent(raw json.RawMessage) string {
	blocks := decodeBlocks(raw)
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func compact(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return s // not valid JSON; keep verbatim
	}
	return buf.String()
}

func compactBlock(b piContentBlock) string {
	data, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return string(data)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseISO(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

func millisToTime(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}
