package agentrunner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"harness-platform/orchestrator/internal/agentjournal"
	"harness-platform/orchestrator/internal/agentwire"
)

// Replay never starts Pi or dispatches a command. It exports committed records
// from the original incarnation after fencing any previous journal reader.
func Replay(ctx context.Context, c Config, owner, after uint64, output io.Writer) error {
	if !validIdentity(c.Session) || !validIdentity(c.Incarnation) || !filepath.IsAbs(c.StateRoot) {
		return agentjournal.ErrConflict
	}
	path := filepath.Join(c.StateRoot, "sessions", c.Session, "incarnations", c.Incarnation, "journal.db")
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 512 {
		return agentjournal.ErrConflict
	}
	if c.JournalBytes == 0 {
		c.JournalBytes = 64 << 20
	}
	journal, err := agentjournal.Open(ctx, path, c.Session, c.Incarnation, c.JournalBytes)
	if err != nil {
		return err
	}
	defer journal.Close()
	if err = journal.Sync(ctx, owner, after); err != nil {
		return err
	}
	for {
		records, err := journal.Records(ctx, owner, after, 128)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			frame := agentwire.Frame{Kind: "record", Session: c.Session, Incarnation: c.Incarnation, Owner: owner, Sequence: record.Sequence, CommandID: record.CommandID, TurnID: record.TurnID, State: record.Kind, Payload: record.Payload}
			if err = agentwire.Encode(output, frame); err != nil {
				return err
			}
			after = record.Sequence
		}
	}
	unresolved, err := journal.Unresolved(ctx)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]int{"unresolved": len(unresolved)})
	return agentwire.Encode(output, agentwire.Frame{Kind: "recovered", Session: c.Session, Incarnation: c.Incarnation, Owner: owner, After: after, Payload: payload})
}
