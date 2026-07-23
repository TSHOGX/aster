package server

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// traceCollector accumulates execution-trace nodes (tool calls, interleaved
// with synthesized "thinking" spans) as pi lifecycle events stream in during a
// turn. drain() returns the nodes assembled since the previous drain and clears
// the buffer, so each persisted assistant message carries the trace that
// produced it. All access is guarded: pi events and message commits arrive on
// the same normalizer goroutine today, but the lock keeps this safe if that
// changes.
type traceCollector struct {
	mu       sync.Mutex
	finished []traceNode          // completed nodes awaiting the next drain
	pending  map[string]traceNode // tool nodes started but not yet ended, by toolCallId
	// lastEventAt marks when the previous node finished (or the turn's first
	// event), so a "thinking" span can be synthesized for the gap before the
	// next tool starts.
	lastEventAt time.Time
	now         func() time.Time
}

// traceNode is one entry in a message's execution trace. Field names are
// camelCase to match the frontend TraceNode contract; the store persists this
// verbatim as a JSON array and never interprets it.
type traceNode struct {
	Kind          string          `json:"kind"` // "thinking" | "tool"
	ToolName      string          `json:"toolName,omitempty"`
	Label         string          `json:"label,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
	ResultSummary string          `json:"resultSummary,omitempty"`
	IsError       bool            `json:"isError,omitempty"`
	StartedAt     string          `json:"startedAt,omitempty"`
	DurationMs    int64           `json:"durationMs,omitempty"`
	Diff          *traceDiff      `json:"diff,omitempty"`
}

// traceDiff carries the high-value before/after for file-mutating tools so the
// client can render an inline diff without re-fetching the workspace.
type traceDiff struct {
	Path    string          `json:"path,omitempty"`
	Content string          `json:"content,omitempty"` // full new content (write)
	Edits   []traceDiffEdit `json:"edits,omitempty"`   // per-hunk old/new (edit)
}

type traceDiffEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

const (
	// Per-node caps so a trace row stays small; large tool output is summarized,
	// not archived. Diffs are the exception callers opt into via traceDiff.
	traceResultMax   = 2000
	traceArgValueMax = 2000
	traceDiffTextMax = 8000
	// A gap shorter than this between nodes is not worth a "thinking" node.
	traceThinkingMinGap = 900 * time.Millisecond
)

func newTraceCollector() *traceCollector {
	return &traceCollector{pending: map[string]traceNode{}, now: time.Now}
}

func (c *traceCollector) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// startTool records the beginning of a tool execution and, when enough time has
// passed since the last activity, a preceding "thinking" span.
func (c *traceCollector) startTool(toolCallID, toolName string, args map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.maybeThinkingLocked(now)
	node := traceNode{
		Kind:      "tool",
		ToolName:  toolName,
		Label:     toolLabel(toolName, args),
		Args:      marshalArgs(args),
		StartedAt: now.UTC().Format(time.RFC3339Nano),
		Diff:      toolDiff(toolName, args),
	}
	if toolCallID == "" {
		// No id to correlate the end event: keep it as an unfinished node keyed
		// by a monotonic placeholder so it still shows up on drain.
		toolCallID = "_anon_" + node.StartedAt
	}
	c.pending[toolCallID] = node
}

// endTool finalizes a previously started tool node with its result + duration.
func (c *traceCollector) endTool(toolCallID string, result any, isError bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	node, ok := c.pending[toolCallID]
	if !ok {
		// End without a matching start (e.g. anon id mismatch): synthesize a
		// minimal node so the result is not lost.
		node = traceNode{Kind: "tool", StartedAt: now.UTC().Format(time.RFC3339Nano)}
	} else {
		delete(c.pending, toolCallID)
	}
	if started, err := time.Parse(time.RFC3339Nano, node.StartedAt); err == nil {
		node.DurationMs = now.Sub(started).Milliseconds()
	}
	node.ResultSummary = summarizeResult(result)
	node.IsError = isError
	c.finished = append(c.finished, node)
	c.lastEventAt = now
}

// maybeThinkingLocked appends a synthesized thinking span covering the gap since
// the last node, when that gap is long enough to be meaningful.
func (c *traceCollector) maybeThinkingLocked(now time.Time) {
	if c.lastEventAt.IsZero() {
		c.lastEventAt = now
		return
	}
	gap := now.Sub(c.lastEventAt)
	if gap >= traceThinkingMinGap {
		c.finished = append(c.finished, traceNode{
			Kind:       "thinking",
			Label:      "Thinking",
			StartedAt:  c.lastEventAt.UTC().Format(time.RFC3339Nano),
			DurationMs: gap.Milliseconds(),
		})
	}
	c.lastEventAt = now
}

// drain returns the nodes accumulated since the last drain (JSON array) and
// resets the buffer. Any still-pending tool nodes are flushed as unfinished so
// a mid-turn message commit does not drop them. Returns nil when empty.
func (c *traceCollector) drain() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	nodes := c.finished
	c.finished = nil
	// Flush unfinished tool nodes (rare: a message committed before the tool end
	// event) so they still render, then forget them.
	for _, node := range c.pending {
		nodes = append(nodes, node)
	}
	c.pending = map[string]traceNode{}
	if len(nodes) == 0 {
		return nil
	}
	encoded, err := json.Marshal(nodes)
	if err != nil {
		return nil
	}
	return encoded
}

func toolLabel(toolName string, args map[string]any) string {
	for _, key := range []string{"path", "file_path", "command", "cmd", "pattern", "query", "url", "name"} {
		if v, ok := args[key].(string); ok {
			if t := strings.TrimSpace(v); t != "" {
				return truncate(t, 120)
			}
		}
	}
	return ""
}

// toolDiff extracts before/after material for file-mutating tools so the client
// can render an inline diff. Returns nil for tools that do not modify files.
func toolDiff(toolName string, args map[string]any) *traceDiff {
	path, _ := args["path"].(string)
	switch toolName {
	case "write":
		content, _ := args["content"].(string)
		if content == "" {
			return nil
		}
		return &traceDiff{Path: path, Content: truncate(content, traceDiffTextMax)}
	case "edit":
		raw, ok := args["edits"].([]any)
		if !ok {
			return nil
		}
		var edits []traceDiffEdit
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			oldText, _ := m["oldText"].(string)
			newText, _ := m["newText"].(string)
			edits = append(edits, traceDiffEdit{
				OldText: truncate(oldText, traceDiffTextMax),
				NewText: truncate(newText, traceDiffTextMax),
			})
		}
		if len(edits) == 0 {
			return nil
		}
		return &traceDiff{Path: path, Edits: edits}
	default:
		return nil
	}
}

// marshalArgs serializes tool args, truncating any oversized string values so a
// pasted file or long command does not bloat the trace row.
func marshalArgs(args map[string]any) json.RawMessage {
	if len(args) == 0 {
		return nil
	}
	trimmed := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok {
			trimmed[k] = truncate(s, traceArgValueMax)
			continue
		}
		trimmed[k] = v
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return nil
	}
	return encoded
}

// summarizeResult reduces an arbitrary tool result to a short text preview.
func summarizeResult(result any) string {
	if result == nil {
		return ""
	}
	switch v := result.(type) {
	case string:
		return truncate(v, traceResultMax)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return truncate(string(encoded), traceResultMax)
	}
}

// truncate clips s to at most max runes, appending an ellipsis marker when cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
