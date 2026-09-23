package agentrunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/agentwire"
)

func TestPromptSettlementIncludesRetryAndCompactionContinuations(t *testing.T) {
	ctx := context.Background()
	r := pressureRunner(t, 4<<20)
	r.native = &pressureNative{}
	settlements := 0
	r.settle = func() error { settlements++; return nil }
	nativeFile := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(nativeFile, []byte("before continuation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "original", TurnID: "turn", Payload: json.RawMessage(`{"type":"prompt","message":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	r.nativeOutput(ctx, strings.NewReader(`{"type":"response","id":"original","success":true}
{"type":"agent_end","willRetry":true,"messages":[{"role":"assistant","stopReason":"error"}]}
{"type":"auto_retry_start","attempt":1}
{"type":"auto_retry_end","success":true}
{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}
{"type":"compaction_start"}
{"type":"compaction_end","willRetry":true}
{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}
`), nativeFile)
	if r.active != "original" || settlements != 0 {
		t.Fatal("loop end prematurely settled native prompt", r.active, settlements)
	}
	pending, err := r.journal.Unresolved(ctx)
	if err != nil || len(pending) != 1 || pending[0].State != "applied" {
		t.Fatal(pending, err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(nativeFile), "captures", "original.jsonl")); !os.IsNotExist(err) {
		t.Fatal("premature native capture", err)
	}
	if err = r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "next", TurnID: "next-turn", Payload: json.RawMessage(`{"type":"prompt","message":"too early"}`)}); err != nil {
		t.Fatal(err)
	}
	if state, err := r.journal.Status(ctx, 1, "next"); err != nil || state != "rejected" {
		t.Fatal("another prompt crossed native continuation", state, err)
	}
	if err = os.WriteFile(nativeFile, []byte("after retry and compaction\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.nativeOutput(ctx, strings.NewReader(`{"type":"prompt_end","id":"original","outcome":"completed"}`+"\n"), nativeFile)
	select {
	case err := <-r.fatal:
		t.Fatal(err)
	default:
	}
	if r.active != "" || settlements != 1 {
		t.Fatal(r.active, settlements)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(nativeFile), "captures", "original.jsonl"))
	if err != nil || string(data) != "after retry and compaction\n" {
		t.Fatal("capture excluded continuation", string(data), err)
	}
	rows, err := r.journal.Records(ctx, 1, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, row := range rows {
		if row.Kind == "native" && (row.CommandID != "original" || row.TurnID != "turn") {
			t.Fatal("native continuation lost original attribution", row)
		}
		if row.Kind == "command.completed" {
			completed++
			var capture Capture
			if json.Unmarshal(row.Payload, &capture) != nil || capture.Outcome != "completed" {
				t.Fatal(string(row.Payload))
			}
		}
	}
	if completed != 1 {
		t.Fatal("prompt completed more than once", completed)
	}
}

func TestNativeSettlementRefusesUncorrelatedOrUnappliedCompletion(t *testing.T) {
	for _, events := range []string{
		`{"type":"prompt_end","id":"original","outcome":"completed"}`,
		"{\"type\":\"response\",\"id\":\"original\",\"success\":true}\n" + `{"type":"prompt_end","id":"foreign","outcome":"completed"}`,
		"{\"type\":\"response\",\"id\":\"original\",\"success\":true}\n" + `{"type":"prompt_end","id":"original","outcome":"maybe"}`,
	} {
		r := pressureRunner(t, 4<<20)
		r.native = &pressureNative{}
		r.settle = func() error { t.Fatal("invalid settlement killed descendants"); return nil }
		if err := r.command(context.Background(), agentwire.Frame{Owner: 1, CommandID: "original", TurnID: "turn", Payload: json.RawMessage(`{"type":"prompt","message":"work"}`)}); err != nil {
			t.Fatal(err)
		}
		r.nativeOutput(context.Background(), strings.NewReader(events+"\n"), filepath.Join(t.TempDir(), "missing.jsonl"))
		select {
		case <-r.fatal:
		default:
			t.Fatal("invalid settlement accepted")
		}
		if state, err := r.journal.Status(context.Background(), 1, "original"); err != nil || state == "completed" {
			t.Fatal(state, err)
		}
	}
}
