package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendTurnControlWritesAVersionedLog(t *testing.T) {
	root := t.TempDir()
	first, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlAbort})
	if err != nil {
		t.Fatalf("append abort: %v", err)
	}
	if first.Token == "" {
		t.Fatal("append must mint a token when the caller supplies none")
	}
	contents, err := ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contents.Version != TurnControlVersion {
		t.Fatalf("version = %d, want %d", contents.Version, TurnControlVersion)
	}
	if len(contents.Commands) != 1 || contents.Commands[0].Command != TurnControlAbort {
		t.Fatalf("commands = %+v", contents.Commands)
	}
}

// Two commands for the same turn both survive. A single-slot file would keep
// only the second, and for steer that is text the user typed being dropped while
// the host has already reported it delivered.
func TestAppendTurnControlAccumulatesWithinOneTurn(t *testing.T) {
	root := t.TempDir()
	for _, message := range []string{"first", "second"} {
		if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlSteer, Message: message}); err != nil {
			t.Fatalf("append %s: %v", message, err)
		}
	}
	contents, err := ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var messages []string
	for _, command := range contents.Commands {
		messages = append(messages, command.Message)
	}
	if len(messages) != 2 || messages[0] != "first" || messages[1] != "second" {
		t.Fatalf("messages = %v, want [first second] in order", messages)
	}
	if contents.Commands[0].Token == contents.Commands[1].Token {
		t.Fatal("tokens must differ or the sandbox applies only the first")
	}
}

// Cleanup is a side effect of writing: the host only ever controls the turn that
// is executing, so entries for any other turn are dropped as it appends. Without
// this the log would grow for the life of the generation and a stale abort would
// sit there being re-evaluated by every later turn's watcher.
func TestAppendTurnControlDropsOtherTurns(t *testing.T) {
	root := t.TempDir()
	if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlAbort}); err != nil {
		t.Fatalf("append turn 7: %v", err)
	}
	if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 8, Command: TurnControlSteer, Message: "now this"}); err != nil {
		t.Fatalf("append turn 8: %v", err)
	}
	contents, err := ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(contents.Commands) != 1 || contents.Commands[0].TurnID != 8 {
		t.Fatalf("commands = %+v, want only turn 8", contents.Commands)
	}
}

func TestAppendTurnControlRejectsUnusableCommands(t *testing.T) {
	root := t.TempDir()
	for _, testCase := range []struct {
		name    string
		command TurnControlCommand
	}{
		{"no turn id", TurnControlCommand{Command: TurnControlAbort}},
		{"unknown command", TurnControlCommand{TurnID: 7, Command: "explode"}},
		// A steer with nothing to inject would confuse the agent and cannot be
		// what the user meant.
		{"empty steer", TurnControlCommand{TurnID: 7, Command: TurnControlSteer, Message: "   "}},
	} {
		if _, err := AppendTurnControl(root, testCase.command); err == nil {
			t.Fatalf("%s: expected an error", testCase.name)
		}
	}
	if _, err := ReadTurnControl(root); err != nil {
		t.Fatalf("a refused append must not leave a broken log: %v", err)
	}
}

// An abort carries no message, so a caller passing one has confused it with
// steer. Clearing it keeps the file honest about what will reach the agent.
func TestAppendTurnControlDropsAnAbortMessage(t *testing.T) {
	root := t.TempDir()
	command, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlAbort, Message: "ignored"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if command.Message != "" {
		t.Fatalf("returned message = %q, want empty", command.Message)
	}
	contents, _ := ReadTurnControl(root)
	if contents.Commands[0].Message != "" {
		t.Fatalf("stored message = %q, want empty", contents.Commands[0].Message)
	}
}

// A version this host does not write means a rootfs/host mismatch. Reading it as
// an empty log and overwriting would discard commands the sandbox may not have
// applied yet, so it is an error the caller can surface.
func TestReadTurnControlRefusesAnUnknownVersion(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"version": TurnControlVersion + 1, "commands": []any{}})
	if err := os.WriteFile(TurnControlPath(root), payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadTurnControl(root); err == nil {
		t.Fatal("expected an unsupported-version error")
	}
	if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlAbort}); err == nil {
		t.Fatal("append must refuse rather than clobber a log it cannot read")
	}
}

// The ceiling is reported, never enforced by dropping the oldest entry: every
// steer holds text the user typed, and silently discarding one would be worse
// than refusing the new one.
func TestAppendTurnControlRefusesPastItsCeiling(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxTurnControlCommands; i++ {
		if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlSteer, Message: "more"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlSteer, Message: "one too many"})
	if err == nil {
		t.Fatal("expected the ceiling to be reported")
	}
	if !strings.Contains(err.Error(), "pending control commands") {
		t.Fatalf("error = %v, want it to name the ceiling", err)
	}
}

// The host is the only writer, but two of its own requests for one session can
// be in flight at once (a Stop racing a steer, or two steers). Without the write
// lock the second's read-modify-write would miss the first append.
func TestAppendTurnControlIsSafeUnderConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	const writers = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlSteer, Message: "concurrent"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}
	contents, err := ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(contents.Commands) != writers {
		t.Fatalf("commands = %d, want %d — an append was lost", len(contents.Commands), writers)
	}
	tokens := map[string]bool{}
	for _, command := range contents.Commands {
		if tokens[command.Token] {
			t.Fatalf("duplicate token %q: the sandbox would apply only one of them", command.Token)
		}
		tokens[command.Token] = true
	}
}

// The control file shares the inbox with seq-named responses. It has to be inert
// there: the queue reader must skip it and the response prune must not delete it.
// Both hinge on the name not parsing as a sequence.
func TestTurnControlFileIsInertInTheInbox(t *testing.T) {
	root := t.TempDir()
	if _, ok := parseSeqFileName(TurnControlFile); ok {
		t.Fatal("the control file name must not parse as a sequence")
	}
	inbox, err := OpenQueue(root, InboxDir)
	if err != nil {
		t.Fatalf("open inbox: %v", err)
	}
	if _, err := AppendTurnControl(root, TurnControlCommand{TurnID: 7, Command: TurnControlAbort}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := inbox.Write(context.Background(), Envelope{Type: TypeNoWork, SessionID: "s", GenerationID: "g"}); err != nil {
		t.Fatalf("write response: %v", err)
	}
	files, err := inbox.ReadAll()
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].Path) == TurnControlFile {
		t.Fatalf("queue reader saw the control file: %+v", files)
	}
	// The write above ran pruneStaleResponses. The control file must have survived
	// it, or a Stop would vanish whenever a response happened to be written.
	if _, err := ReadTurnControl(root); err != nil {
		t.Fatalf("control log did not survive an inbox write: %v", err)
	}
}
