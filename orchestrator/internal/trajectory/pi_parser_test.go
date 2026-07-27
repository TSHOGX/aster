package trajectory

import (
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
)

// A compact but representative native pi session: head, model_change, a user
// turn, an assistant turn with thinking + a tool call, and the matching
// toolResult (one erroring). Mirrors the real on-disk shape.
const goldenPiSession = `{"type":"session","version":3,"id":"sess-abc","timestamp":"2026-07-14T03:33:33.071Z","cwd":"/workspace","parent_session":null}
{"type":"model_change","id":"mc1","parentId":"sess-abc","timestamp":"2026-07-14T03:33:33.100Z","provider":"anthropic","modelId":"sonnet"}
{"type":"message","id":"u1","parentId":"mc1","timestamp":"2026-07-14T03:33:34.000Z","message":{"role":"user","content":[{"type":"text","text":"list the files"}],"timestamp":1784000014000}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-07-14T03:33:35.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"I should run ls","thinkingSignature":"sig"},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"cmd":"ls"}}],"timestamp":1784000015000}}
{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-07-14T03:33:36.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","isError":false,"content":[{"type":"text","text":"a.txt\nb.txt"}],"timestamp":1784000016000}}
{"type":"message","id":"a2","parentId":"t1","timestamp":"2026-07-14T03:33:37.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_2","name":"bash","arguments":{"cmd":"cat missing"}}],"timestamp":1784000017000}}
{"type":"message","id":"t2","parentId":"a2","timestamp":"2026-07-14T03:33:38.000Z","message":{"role":"toolResult","toolCallId":"call_2","toolName":"bash","isError":true,"content":[{"type":"text","text":"no such file"}],"timestamp":1784000018000}}
{"type":"message","id":"a3","parentId":"t2","timestamp":"2026-07-14T03:33:39.000Z","message":{"role":"assistant","content":[{"type":"text","text":"There are two files."}],"timestamp":1784000019000}}
`

func TestParsePiSessionGolden(t *testing.T) {
	events, meta, err := ParsePiSession(strings.NewReader(goldenPiSession))
	if err != nil {
		t.Fatalf("ParsePiSession: %v", err)
	}

	// Expected ordered kind sequence.
	wantKinds := []string{
		"system",      // model_change
		"user",        // list the files
		"thinking",    // I should run ls
		"tool_call",   // call_1
		"tool_result", // call_1 result
		"tool_call",   // call_2
		"tool_result", // call_2 error
		"assistant",   // There are two files.
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d; kinds=%v", len(events), len(wantKinds), kindsOf(events))
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("event[%d].Kind = %q, want %q (all=%v)", i, events[i].Kind, want, kindsOf(events))
		}
	}

	// model_change is a system event with the right subtype.
	if events[0].Subtype != "model_change" {
		t.Fatalf("event[0].Subtype = %q, want model_change", events[0].Subtype)
	}
	// thinking is verbatim, no subtype.
	if events[2].Text != "I should run ls" || events[2].Subtype != "" {
		t.Fatalf("thinking event wrong: %+v", events[2])
	}
	// tool_call text is the full args JSON; call_id links to the result.
	if !strings.Contains(events[3].Text, "ls") || events[3].CallID != "call_1" || events[3].Name != "bash" {
		t.Fatalf("tool_call event wrong: %+v", events[3])
	}
	if events[4].CallID != "call_1" || events[4].IsError {
		t.Fatalf("tool_result 1 should link call_1 and not error: %+v", events[4])
	}
	// second tool_result is an error.
	if events[6].CallID != "call_2" || !events[6].IsError {
		t.Fatalf("tool_result 2 should link call_2 and be an error: %+v", events[6])
	}
	// full text preserved, not truncated.
	if events[4].Text != "a.txt\nb.txt" {
		t.Fatalf("tool_result text not preserved: %q", events[4].Text)
	}
	// Tree-A lineage carried through.
	if events[1].NativeID != "u1" || events[1].ParentID != "mc1" {
		t.Fatalf("lineage wrong on user event: native=%q parent=%q", events[1].NativeID, events[1].ParentID)
	}

	// Meta.
	if meta.NativeSessionID != "sess-abc" {
		t.Fatalf("NativeSessionID = %q", meta.NativeSessionID)
	}
	if meta.Provider != "anthropic" || meta.Model != "sonnet" {
		t.Fatalf("provider/model = %q/%q", meta.Provider, meta.Model)
	}
	if meta.CWD != "/workspace" {
		t.Fatalf("cwd = %q", meta.CWD)
	}
	// 4 user|assistant message records (u1, a1, a2, a3).
	if meta.TurnCount != 4 {
		t.Fatalf("TurnCount = %d, want 4", meta.TurnCount)
	}
	if meta.RecordCount != 8 {
		t.Fatalf("RecordCount = %d, want 8", meta.RecordCount)
	}
	if meta.IsTest {
		t.Fatalf("clean session flagged as test")
	}
	// fingerprint = "<records>:<lastEpoch>"; last record ts 2026-07-14T03:33:39Z.
	if !strings.HasPrefix(meta.Fingerprint, "8:") {
		t.Fatalf("fingerprint = %q, want prefix 8:", meta.Fingerprint)
	}
	// No usage events ever.
	for _, e := range events {
		if e.Kind == "usage" {
			t.Fatalf("unexpected usage event")
		}
	}
}

func TestParsePiSessionFauxIsTest(t *testing.T) {
	faux := `{"type":"session","version":3,"id":"s","timestamp":"2026-07-14T03:33:33.071Z","cwd":"/tmp"}
{"type":"model_change","id":"m","parentId":"s","timestamp":"2026-07-14T03:33:33.100Z","provider":"faux","modelId":"test"}
`
	_, meta, err := ParsePiSession(strings.NewReader(faux))
	if err != nil {
		t.Fatalf("ParsePiSession: %v", err)
	}
	if !meta.IsTest {
		t.Fatalf("faux-provider session should be flagged IsTest")
	}
}

func kindsOf(events []memorystore.TrajectoryEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}
