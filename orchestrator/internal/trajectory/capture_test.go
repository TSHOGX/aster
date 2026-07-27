package trajectory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/controlsource"
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// writeNativePiFile drops a native pi session JSONL at
// <home>/.pi/agent/sessions/<rel> and returns the abs path.
func writeNativePiFile(t *testing.T, home, rel, content string) string {
	t.Helper()
	full := filepath.Join(home, ".pi", "agent", "sessions", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write native: %v", err)
	}
	return full
}

// newCaptureTestStore opens the control+memory database pair capture needs since
// the split: the session and its driver home are seeded on the control side, the
// captured trajectory is asserted on the memory side, and the two are joined only
// by the ingest boundary.
func newCaptureTestStore(t *testing.T) (*store.Store, *memorystore.Store, *memorystore.Ingestor, context.Context) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	control, err := store.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })

	mem, err := memorystore.Open(ctx, filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	if err := control.EnsureUser(ctx, "u1", "User One"); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	return control, mem, memorystore.NewIngestor(mem, controlsource.New(control)), ctx
}

func TestCaptureSessionFullFidelity(t *testing.T) {
	control, mem, ingest, ctx := newCaptureTestStore(t)

	const sessionID = "sess_cap"
	now := time.Now().UTC()
	if err := control.CreateSession(ctx, store.Session{
		ID: sessionID, UserID: "u1", Status: string(sessionstate.Created),
		Agent: "pi", Workspace: t.TempDir(), RestoreID: "r-" + sessionID,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	home := t.TempDir()
	// Full tool result text includes a >2000-char blob to prove NO truncation.
	bigOutput := ""
	for i := 0; i < 3000; i++ {
		bigOutput += "x"
	}
	native := `{"type":"session","version":3,"id":"pi-xyz","timestamp":"2026-07-14T03:33:33.071Z","cwd":"/workspace"}
{"type":"message","id":"u1","parentId":"pi-xyz","timestamp":"2026-07-14T03:33:34Z","message":{"role":"user","content":[{"type":"text","text":"go"}],"timestamp":1784000014000}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-07-14T03:33:35Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_1","name":"bash","arguments":{"cmd":"ls"}}],"timestamp":1784000015000}}
{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-07-14T03:33:36Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","isError":false,"content":[{"type":"text","text":"` + bigOutput + `"}],"timestamp":1784000016000}}
`
	writeNativePiFile(t, home, "2026-07-14T03-33-33_pi-xyz.jsonl", native)

	archiveRoot := t.TempDir()
	cap := NewCapturer(mem, ingest, archiveRoot, nil)
	if cap == nil {
		t.Fatal("NewCapturer returned nil for non-empty root")
	}

	res, err := cap.CaptureSession(ctx, memorystore.PiSessionForCapture{
		SessionID:              sessionID,
		UserID:                 "u1",
		Status:                 string(sessionstate.Created),
		AgentHomeHostPath:      home,
		SelectedSessionRelpath: "2026-07-14T03-33-33_pi-xyz.jsonl",
		NativeSessionID:        "pi-xyz",
	})
	if err != nil {
		t.Fatalf("CaptureSession: %v", err)
	}
	if res.Fidelity != "full" {
		t.Fatalf("fidelity = %q, want full (reason: %s)", res.Fidelity, res.Reason)
	}

	// Events persisted with full fidelity.
	events, err := mem.GetTrajectoryEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetTrajectoryEvents: %v", err)
	}
	wantKinds := []string{"user", "tool_call", "tool_result"}
	if len(events) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d", len(events), len(wantKinds))
	}
	for i, w := range wantKinds {
		if events[i].Kind != w {
			t.Fatalf("event[%d].Kind = %q, want %q", i, events[i].Kind, w)
		}
	}
	// The tool_result kept the full >2000-char text (no product-side truncation).
	if len(events[2].Text) < 3000 {
		t.Fatalf("tool_result text truncated: len=%d, want >=3000", len(events[2].Text))
	}
	// call_id linkage preserved, native lineage carried.
	if events[1].CallID != "call_1" || events[2].CallID != "call_1" {
		t.Fatalf("call_id linkage lost: %q / %q", events[1].CallID, events[2].CallID)
	}
	if events[0].NativeID != "u1" || events[0].ParentID != "pi-xyz" {
		t.Fatalf("native lineage lost: %+v", events[0])
	}

	// raw_sources archive copy exists on disk.
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Fatalf("archive not written: %v", err)
	}

	// Idempotent re-capture: same events, no error.
	res2, err := cap.CaptureSession(ctx, memorystore.PiSessionForCapture{
		SessionID: sessionID, UserID: "u1", Status: string(sessionstate.Created),
		AgentHomeHostPath: home, SelectedSessionRelpath: "2026-07-14T03-33-33_pi-xyz.jsonl", NativeSessionID: "pi-xyz",
	})
	if err != nil {
		t.Fatalf("re-capture: %v", err)
	}
	if res2.EventCount != res.EventCount {
		t.Fatalf("re-capture event count drift: %d != %d", res2.EventCount, res.EventCount)
	}
}
