package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakePi writes a stub `pi` executable that emits the given stdout bodies on
// successive invocations (tracked via a counter file), so the retry path can be
// exercised without touching a real model.
func fakePi(t *testing.T, bodies ...string) string {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := "#!/usr/bin/env bash\nn=0\n[[ -f " + counter + " ]] && n=$(cat " + counter + ")\nn=$((n+1))\necho $n > " + counter + "\ncase $n in\n"
	for i, body := range bodies {
		script += fmt.Sprintf("  %d) cat <<'PIEOF'\n%s\nPIEOF\n  ;;\n", i+1, body)
	}
	script += "  *) echo '{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"exhausted\"}]}}' ;;\nesac\n"
	bin := filepath.Join(dir, "pi")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return bin
}

// streamEnd renders a pi --mode json message_end event carrying text.
func streamEnd(text string) string {
	raw, _ := json.Marshal(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	})
	return string(raw)
}

// A reply that narrates instead of emitting JSON is a transient shape slip; the
// runner must retry once rather than degrading the node for the whole cycle.
func TestAgentRunRetriesOnMissingJSON(t *testing.T) {
	bin := fakePi(t,
		streamEnd("Sure, let me think about the clustering problem out loud."),
		streamEnd(`Here is the answer: {"canonical":{"doris":"doris"}}`),
	)
	r := NewAgentRunner(AgentConfig{Bin: bin, Provider: "modelarts", Model: "glm-5.2"})

	payload, err := r.Run(context.Background(), "sys", "data", false)
	if err != nil {
		t.Fatalf("Run should have recovered on retry: %v", err)
	}
	if payload != `{"canonical":{"doris":"doris"}}` {
		t.Fatalf("payload = %q", payload)
	}
}

// Two bad replies in a row still fail — retry is one extra attempt, not a loop.
func TestAgentRunGivesUpAfterOneRetry(t *testing.T) {
	bin := fakePi(t, streamEnd("no json here"), streamEnd("still no json"))
	r := NewAgentRunner(AgentConfig{Bin: bin, Provider: "modelarts", Model: "glm-5.2"})

	if _, err := r.Run(context.Background(), "sys", "data", false); err == nil {
		t.Fatal("expected failure after the retry also came back shapeless")
	} else if !isShapeError(err) {
		t.Fatalf("want a shape error, got %v", err)
	}
}

// A cancelled context must not be retried.
func TestAgentRunDoesNotRetryCancelledContext(t *testing.T) {
	bin := fakePi(t, streamEnd("no json"), streamEnd(`{"ok":true}`))
	r := NewAgentRunner(AgentConfig{Bin: bin, Provider: "modelarts", Model: "glm-5.2"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, "sys", "data", false); err == nil {
		t.Fatal("expected an error on a cancelled context")
	}
}
