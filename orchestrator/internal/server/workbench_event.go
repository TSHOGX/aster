package server

import (
	"encoding/json"
	"harness-platform/orchestrator/internal/events"
)

// Workbench projection is computed after public sanitization, separately from
// the legacy event cache. Durable storage and legacy subscriptions are unchanged.
func workbenchEvent(e events.Event) events.Event {
	switch e.Type {
	case "emit_output":
		e.Payload = nil
	case "system.status":
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return e
		}
		var p map[string]any
		if json.Unmarshal(b, &p) != nil {
			return e
		}
		e.Payload = map[string]any{"type": p["type"]}
	case "agent.message", "message.created":
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return e
		}
		var p map[string]any
		if json.Unmarshal(b, &p) != nil {
			return e
		}
		if text, ok := p["content"].(string); ok {
			r := []rune(text)
			p["content_length"] = len(r)
			p["content_truncated"] = len(r) > 4096
			if len(r) > 4096 {
				p["content"] = string(r[:2048])
			}
		}
		trace, exists := p["trace"]
		p["has_trace"] = exists && trace != nil
		delete(p, "trace")
		e.Payload = p
	case "agent.output":
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return e
		}
		var p map[string]any
		if json.Unmarshal(b, &p) != nil {
			return e
		}
		typ, _ := p["type"].(string)
		switch typ {
		case "tool_execution_start", "tool_execution_update", "tool_execution_end":
			out := map[string]any{"type": typ}
			for _, key := range []string{"toolCallId", "toolName", "isError"} {
				if v, ok := p[key]; ok {
					out[key] = v
				}
			}
			if args, ok := p["args"].(map[string]any); ok {
				small := map[string]any{}
				for _, key := range []string{"path", "file_path", "command"} {
					if text, ok := args[key].(string); ok {
						r := []rune(text)
						if len(r) > 200 {
							r = r[:200]
						}
						small[key] = string(r)
					}
				}
				out["args"] = small
			}
			// Full args/results remain in lazy trace.
			e.Payload = out
		default:
			e.Payload = map[string]any{"type": typ}
		}
	}
	return e
}
