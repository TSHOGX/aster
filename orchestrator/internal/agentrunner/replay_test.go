package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/agentjournal"
	"harness-platform/orchestrator/internal/agentwire"
)

func TestRecoveryReplayDoesNotLaunchPiOrReissueUncertainCommand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s", "incarnations", "i", "journal.db")
	journal, err := agentjournal.Open(ctx, path, "s", "i", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Sync(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	command := agentjournal.Command{ID: "original", TurnID: "turn", Kind: "prompt", Payload: json.RawMessage(`{"type":"prompt","message":"unknown native effect"}`)}
	if _, _, err = journal.Receive(ctx, 1, command); err != nil {
		t.Fatal(err)
	}
	if err = journal.Dispatch(ctx, 1, command.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Append(ctx, agentjournal.Record{CommandID: command.ID, TurnID: command.TurnID, Kind: "native", Payload: json.RawMessage(`{"type":"message_update"}`)}); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = Replay(ctx, Config{Session: "s", Incarnation: "i", StateRoot: root, PiBinary: "/does/not/exist"}, 2, 0, &output); err != nil {
		t.Fatal(err)
	}
	decoder := agentwire.NewDecoder(&output)
	record, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != "record" || record.CommandID != "original" || record.Owner != 2 {
		t.Fatal(record)
	}
	end, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if end.Kind != "recovered" || end.After != 1 || string(end.Payload) != `{"unresolved":1}` {
		t.Fatal(end)
	}
	// A nonexistent original journal is an error, never a new empty journal that
	// would erase evidence of whether a command was dispatched.
	err = Replay(ctx, Config{Session: "missing", Incarnation: "i", StateRoot: root}, 3, 0, &output)
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "sessions", "missing")); !os.IsNotExist(err) {
		t.Fatal("recovery fabricated new journal storage")
	}
}
