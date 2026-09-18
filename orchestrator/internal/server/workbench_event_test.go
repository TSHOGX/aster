package server

import (
	"encoding/json"
	"harness-platform/orchestrator/internal/events"
	"strings"
	"testing"
)

func TestWorkbenchProjectionDoesNotMutateLegacyEvent(t *testing.T) {
	payload := map[string]any{"id": 1, "content": strings.Repeat("🔋", 10000), "trace": []any{map[string]any{"kind": "tool"}}, "stream_ids": []string{"m1"}}
	projected := workbenchEvent(events.Event{Type: "agent.message", Payload: payload})
	p := projected.Payload.(map[string]any)
	if len([]rune(p["content"].(string))) != 2048 || p["has_trace"] != true || p["trace"] != nil || p["stream_ids"] == nil {
		t.Fatalf("projection %+v", p)
	}
	if len([]rune(payload["content"].(string))) != 10000 || payload["trace"] == nil {
		t.Fatal("mutated legacy payload")
	}
	b, _ := json.Marshal(workbenchEvent(events.Event{Type: "agent.output", Payload: map[string]any{"type": "tool_execution_end", "result": strings.Repeat("secret", 1000), "toolName": "read"}}))
	if strings.Contains(string(b), "secret") {
		t.Fatal("eager tool result")
	}
}
