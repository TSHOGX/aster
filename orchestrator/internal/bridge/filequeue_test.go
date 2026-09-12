package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureLayoutCreatesBridgeTransportDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bridge", "gen_a")
	if err := EnsureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	for _, name := range []string{SandboxTmpDir, HostTmpDir, InboxDir, OutboxDir, HeartbeatDir, HostHeartbeatDir} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", name)
		}
	}
	for _, name := range []string{InboxDir, HostTmpDir, HostHeartbeatDir} {
		info, err := os.Stat(HostOwnedPath(root, name))
		if err != nil {
			t.Fatalf("stat host-owned %s: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("host-owned %s is not a directory", name)
		}
	}
}

func TestQueueWriteReadAndUnlinkUsesOrderedSequenceFiles(t *testing.T) {
	root := t.TempDir()
	queue, err := OpenQueue(root, OutboxDir)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	payload := json.RawMessage(`{"status":"ok"}`)
	first, err := queue.Write(context.Background(), Envelope{
		RequestID:    "req_a",
		Type:         "hello",
		SessionID:    "sess_a",
		GenerationID: "gen_a",
		Payload:      payload,
	})
	if err != nil {
		t.Fatalf("write first: %v", err)
	}
	second, err := queue.Write(context.Background(), Envelope{
		RequestID:    "req_b",
		Type:         "heartbeat",
		SessionID:    "sess_a",
		GenerationID: "gen_a",
	})
	if err != nil {
		t.Fatalf("write second: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("unexpected seqs: first=%d second=%d", first, second)
	}
	if _, err := os.Stat(filepath.Join(root, OutboxDir, "00000000000000000001.json")); err != nil {
		t.Fatalf("first seq file missing: %v", err)
	}

	files, err := queue.ReadAll()
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(files) != 2 || files[0].Seq != 1 || files[1].Seq != 2 {
		t.Fatalf("unexpected files: %+v", files)
	}
	if files[0].Envelope.MessageID == "" || files[0].Envelope.RequestID != "req_a" || !sameJSON(files[0].Envelope.Payload, payload) {
		t.Fatalf("unexpected first envelope: %+v", files[0].Envelope)
	}
	if err := files[0].Unlink(); err != nil {
		t.Fatalf("unlink first: %v", err)
	}
	files, err = queue.ReadAll()
	if err != nil {
		t.Fatalf("read after unlink: %v", err)
	}
	if len(files) != 1 || files[0].Seq != 2 {
		t.Fatalf("unexpected files after unlink: %+v", files)
	}
}

func sameJSON(a, b json.RawMessage) bool {
	var av any
	var bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	encodedA, err := json.Marshal(av)
	if err != nil {
		return false
	}
	encodedB, err := json.Marshal(bv)
	if err != nil {
		return false
	}
	return string(encodedA) == string(encodedB)
}

func TestQueueIgnoresInvalidNamesAndContinuesFromMaxSequence(t *testing.T) {
	root := t.TempDir()
	queue, err := OpenQueue(root, OutboxDir)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, OutboxDir, "not-a-message.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write invalid name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, OutboxDir, "00000000000000000003.json"), []byte(`{"message_id":"m","type":"hello","session_id":"s","generation_id":"g"}`), 0o644); err != nil {
		t.Fatalf("write seq 3: %v", err)
	}
	seq, err := queue.Write(context.Background(), Envelope{
		Type:         "claim_next_turn",
		SessionID:    "s",
		GenerationID: "g",
	})
	if err != nil {
		t.Fatalf("write queue: %v", err)
	}
	if seq != 4 {
		t.Fatalf("seq=%d want 4", seq)
	}
}

func TestInboxWritePrunesOnlyStaleResponsesAndKeepsSequenceMonotonic(t *testing.T) {
	root := t.TempDir()
	queue, err := OpenQueue(root, InboxDir)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	inboxDir := HostOwnedPath(root, InboxDir)
	write := func(name, id string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(inboxDir, name), []byte(`{"message_id":"`+id+`","type":"no_work","session_id":"s","generation_id":"g"}`), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	stale := time.Now().Add(-2 * inboxResponseRetention)
	write("00000000000000000001.json", "stale")
	write("00000000000000000002.json", "stale-too")
	write("00000000000000000003.json", "fresh") // just written: the sandbox may be reading it
	for _, name := range []string{"00000000000000000001.json", "00000000000000000002.json"} {
		if err := os.Chtimes(filepath.Join(inboxDir, name), stale, stale); err != nil {
			t.Fatalf("age %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(inboxDir, "not-a-message.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write invalid name: %v", err)
	}
	seq, err := queue.Write(context.Background(), Envelope{
		MessageID:    "new",
		Type:         "no_work",
		SessionID:    "s",
		GenerationID: "g",
	})
	if err != nil {
		t.Fatalf("write inbox: %v", err)
	}
	// Continues from the highest existing sequence: the sandbox skips anything
	// at or below the last sequence it consumed, so numbers must not restart.
	if seq != 4 {
		t.Fatalf("seq=%d want 4", seq)
	}
	files, err := queue.ReadAll()
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	var ids []string
	for _, file := range files {
		ids = append(ids, file.Envelope.MessageID)
	}
	if len(ids) != 2 || ids[0] != "fresh" || ids[1] != "new" {
		t.Fatalf("stale responses should be pruned and the fresh one kept: %v", ids)
	}
	if _, err := os.Stat(filepath.Join(inboxDir, "not-a-message.json")); err != nil {
		t.Fatalf("invalid non-sequence file should be left alone: %v", err)
	}
}

// The newest response survives even when it is old: it anchors nextSeq.
func TestInboxPruneKeepsNewestResponseAsSequenceAnchor(t *testing.T) {
	root := t.TempDir()
	queue, err := OpenQueue(root, InboxDir)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	inboxDir := HostOwnedPath(root, InboxDir)
	path := filepath.Join(inboxDir, "00000000000000000007.json")
	if err := os.WriteFile(path, []byte(`{"message_id":"old","type":"no_work","session_id":"s","generation_id":"g"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	seq, err := queue.Write(context.Background(), Envelope{MessageID: "new", Type: "no_work", SessionID: "s", GenerationID: "g"})
	if err != nil {
		t.Fatalf("write inbox: %v", err)
	}
	if seq != 8 {
		t.Fatalf("seq=%d want 8", seq)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("newest pre-existing response must be kept: %v", err)
	}
}

func TestTouchHeartbeatWritesDurableMtimeFile(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 25, 12, 34, 56, 123, time.UTC)
	if err := TouchHeartbeat(root, BridgeHeartbeatFile, now); err != nil {
		t.Fatalf("touch bridge heartbeat: %v", err)
	}
	path := filepath.Join(root, HeartbeatDir, BridgeHeartbeatFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bridge heartbeat: %v", err)
	}
	if string(data) != now.Format(time.RFC3339Nano)+"\n" {
		t.Fatalf("heartbeat payload=%q want %q", data, now.Format(time.RFC3339Nano)+"\n")
	}
	firstInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bridge heartbeat: %v", err)
	}
	later := now.Add(time.Second)
	if err := TouchHeartbeat(root, BridgeHeartbeatFile, later); err != nil {
		t.Fatalf("touch bridge heartbeat again: %v", err)
	}
	secondInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bridge heartbeat again: %v", err)
	}
	if secondInfo.ModTime().Before(firstInfo.ModTime()) {
		t.Fatalf("heartbeat mtime moved backwards: first=%s second=%s", firstInfo.ModTime(), secondInfo.ModTime())
	}

	if err := TouchHeartbeat(root, HostHeartbeatFile, now); err != nil {
		t.Fatalf("touch host heartbeat: %v", err)
	}
	hostPath := HeartbeatPath(root, HostHeartbeatFile)
	if hostPath != filepath.Join(HostOwnedPath(root, HostHeartbeatDir), HostHeartbeatFile) {
		t.Fatalf("host heartbeat path=%q", hostPath)
	}
	if _, err := os.Stat(hostPath); err != nil {
		t.Fatalf("host heartbeat missing: %v", err)
	}
}

func TestTouchAndClearCheckpointReady(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 25, 12, 34, 56, 123, time.UTC)
	if err := TouchCheckpointReady(root, now); err != nil {
		t.Fatalf("touch checkpoint ready: %v", err)
	}
	path := filepath.Join(root, HeartbeatDir, CheckpointReadyFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checkpoint ready: %v", err)
	}
	if string(data) != now.Format(time.RFC3339Nano)+"\n" {
		t.Fatalf("checkpoint ready payload=%q want %q", data, now.Format(time.RFC3339Nano)+"\n")
	}
	if err := ClearCheckpointReady(root); err != nil {
		t.Fatalf("clear checkpoint ready: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkpoint ready file should be removed, err=%v", err)
	}
}
