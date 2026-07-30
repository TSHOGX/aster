package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A payload larger than a single argv entry may hold (Linux MAX_ARG_STRLEN,
// 128 KiB) must still reach the agent. Passing it as an argument fails the exec
// outright with E2BIG — which is how the critic silently never ran: it reviews the
// whole accepted set in one turn, and a critic failure reads as "no veto".
func TestAgentRunSpillsOversizePayloadToFile(t *testing.T) {
	// The stub echoes back whether it was handed an @file reference, proving the
	// large payload took the spill path rather than argv.
	script := `#!/usr/bin/env bash
last="${@: -1}"
case "$last" in
  @*) path="${last#@}"
      if [[ -s "$path" ]]; then echo '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"{\"via\":\"file\"}"}]}}'
      else echo '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"{\"via\":\"empty\"}"}]}}'; fi ;;
  *)  echo '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"{\"via\":\"argv\"}"}]}}' ;;
esac
`
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewAgentRunner(AgentConfig{Bin: bin, Provider: "modelarts", Model: "glm-5.2"})

	big := strings.Repeat("政策数据 candidate statement. ", 20000) // well past 64 KiB
	if len(big) <= maxArgPayload {
		t.Fatalf("test payload too small: %d bytes", len(big))
	}
	payload, err := r.Run(context.Background(), "sys", big, false)
	if err != nil {
		t.Fatalf("Run with oversize payload: %v", err)
	}
	if payload != `{"via":"file"}` {
		t.Fatalf("oversize payload should spill to a file, got %s", payload)
	}

	// A small payload keeps the plain argv path — no needless temp files.
	payload, err = r.Run(context.Background(), "sys", "small", false)
	if err != nil {
		t.Fatalf("Run with small payload: %v", err)
	}
	if payload != `{"via":"argv"}` {
		t.Fatalf("small payload should stay in argv, got %s", payload)
	}
}

// The spilled prompt must not be world-readable, and must not outlive the call.
func TestWriteTempPromptIsPrivateAndCleanedUp(t *testing.T) {
	path, cleanup, err := writeTempPrompt("trajectory text with user data")
	if err != nil {
		t.Fatalf("writeTempPrompt: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("temp prompt mode = %o, want 600", perm)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp prompt should be removed, stat err = %v", err)
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
