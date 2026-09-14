package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Host → sandbox control for the turn executing right now.
//
// The bridge proper is request/response with the sandbox as the only requester,
// and during a turn nothing is polling the inbox — the turn thread is blocked
// reading the agent. So a Stop cannot travel as a bridge message. Instead the
// host writes this file and a watcher thread inside the sandbox reads it.
//
// It lives in the inbox, which is already mounted read-only into the sandbox,
// and its name is deliberately not a seq name: both queue readers accept only
// the fixed-width `00000000000000000001.json` form, and pruneStaleResponses only
// ever deletes seq-named files. So the file is invisible to the queue machinery
// and safe from its cleanup — no new mount and no sandbox-contract change.
const TurnControlFile = "turn-control.json"

// TurnControlVersion is the file's schema version. The sandbox refuses anything
// else rather than guessing, so a host and a rootfs that disagree fail visibly
// instead of dropping the user's Stop on the floor.
const TurnControlVersion = 1

const (
	// TurnControlAbort ends the running turn. Whatever the agent already streamed
	// stays in its session and in the transcript; the agent process survives.
	TurnControlAbort = "abort"
	// TurnControlSteer injects a message into the running turn. It does NOT
	// abort: the agent sees the new instruction without discarding the work it
	// has already done.
	TurnControlSteer = "steer"
)

// maxTurnControlCommands bounds the file. Commands accumulate only within one
// turn (see AppendTurnControl), so this is a ceiling on how many times a user
// can steer a single turn, not on session lifetime. Hitting it is reported
// rather than silently dropping the oldest, because every steer carries text the
// user typed.
const maxTurnControlCommands = 64

// TurnControlCommand is one host instruction for the running turn.
type TurnControlCommand struct {
	// Token makes delivery at-most-once. The sandbox cannot delete this file (the
	// inbox is read-only to it), so it instead remembers the tokens it has
	// applied. Uniqueness is the whole contract — ordering comes from position in
	// Commands, not from the token.
	Token string `json:"token"`
	// TurnID stops a command from reaching a turn it was not meant for: the
	// watcher applies an entry only while that turn is the one executing.
	TurnID  int64  `json:"turn_id"`
	Command string `json:"command"`
	Message string `json:"message,omitempty"`
}

// TurnControlFileContents is the on-disk shape: an ordered log, not a single
// command.
//
// A single-slot file loses commands. The watcher polls every few hundred ms, so
// two commands written inside one interval would leave only the second — and for
// steer that is a message the user typed, silently dropped while the host has
// already recorded it as delivered. An append-only log within the turn removes
// that window: every entry the watcher has not yet applied is still there when
// it next looks.
type TurnControlFileContents struct {
	Version  int                  `json:"version"`
	Commands []TurnControlCommand `json:"commands"`
}

// turnControlWriteMu serializes the read-modify-write below. The host is the
// only writer, but two of its own requests (a Stop and a steer, or two steers)
// can be in flight for the same session at once, and without this the second
// read could miss the first append. One mutex for all bridge dirs is deliberate:
// the critical section is a small file rewrite on a user-initiated action, so
// contention is not a real cost, and per-root locks would need their own
// lifecycle.
var turnControlWriteMu sync.Mutex

// AppendTurnControl adds one command to the control log for a running turn.
//
// Entries for any other turn are dropped as it writes. That is the whole cleanup
// story: the host only ever controls the turn currently executing, and a turn
// that has ended can never be controlled again, so the file stays bounded to one
// turn without a separate reaper or a teardown hook to forget.
func AppendTurnControl(root string, command TurnControlCommand) (TurnControlCommand, error) {
	if strings.TrimSpace(root) == "" {
		return TurnControlCommand{}, fmt.Errorf("bridge root is required")
	}
	if command.TurnID <= 0 {
		return TurnControlCommand{}, fmt.Errorf("turn control requires a turn id")
	}
	switch command.Command {
	case TurnControlAbort:
		command.Message = ""
	case TurnControlSteer:
		if strings.TrimSpace(command.Message) == "" {
			return TurnControlCommand{}, fmt.Errorf("turn control steer requires a message")
		}
	default:
		return TurnControlCommand{}, fmt.Errorf("unsupported turn control command %q", command.Command)
	}
	if strings.TrimSpace(command.Token) == "" {
		command.Token = uuid.NewString()
	}
	if err := EnsureLayout(root); err != nil {
		return TurnControlCommand{}, err
	}

	turnControlWriteMu.Lock()
	defer turnControlWriteMu.Unlock()

	existing, err := readTurnControlFile(root)
	if err != nil {
		return TurnControlCommand{}, err
	}
	contents := TurnControlFileContents{Version: TurnControlVersion}
	for _, previous := range existing.Commands {
		if previous.TurnID == command.TurnID {
			contents.Commands = append(contents.Commands, previous)
		}
	}
	if len(contents.Commands) >= maxTurnControlCommands {
		return TurnControlCommand{}, fmt.Errorf("turn %d already has %d pending control commands", command.TurnID, len(contents.Commands))
	}
	contents.Commands = append(contents.Commands, command)

	payload, err := json.MarshalIndent(contents, "", "  ")
	if err != nil {
		return TurnControlCommand{}, err
	}
	payload = append(payload, '\n')
	targetDir := HostOwnedPath(root, InboxDir)
	tmpPath := filepath.Join(HostOwnedPath(root, HostTmpDir), uuid.NewString()+".turn-control")
	if err := writeFileDurable(tmpPath, payload, 0o644); err != nil {
		return TurnControlCommand{}, err
	}
	defer func() { _ = os.Remove(tmpPath) }()
	// Rename, so a watcher polling mid-write never sees a partial log.
	if err := os.Rename(tmpPath, filepath.Join(targetDir, TurnControlFile)); err != nil {
		return TurnControlCommand{}, err
	}
	if err := syncDir(targetDir); err != nil {
		return TurnControlCommand{}, err
	}
	return command, nil
}

// TurnControlPath is where the host writes and the sandbox reads the log.
func TurnControlPath(root string) string {
	return filepath.Join(HostOwnedPath(root, InboxDir), TurnControlFile)
}

// ReadTurnControl reports the current control log. Only the host's own tests and
// diagnostics need this; the sandbox reads the file directly.
func ReadTurnControl(root string) (TurnControlFileContents, error) {
	if strings.TrimSpace(root) == "" {
		return TurnControlFileContents{}, fmt.Errorf("bridge root is required")
	}
	return readTurnControlFile(root)
}

// readTurnControlFile treats a missing file as an empty log: the first command of
// a generation has nothing to append to.
//
// A malformed or wrong-version file is an error rather than an implicit reset.
// Overwriting it would discard commands the sandbox may not have applied yet,
// and the only way it can be malformed is a host/rootfs mismatch — which the
// caller should surface, not paper over.
func readTurnControlFile(root string) (TurnControlFileContents, error) {
	data, err := os.ReadFile(TurnControlPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return TurnControlFileContents{Version: TurnControlVersion}, nil
		}
		return TurnControlFileContents{}, err
	}
	var contents TurnControlFileContents
	if err := json.Unmarshal(data, &contents); err != nil {
		return TurnControlFileContents{}, fmt.Errorf("read turn control log: %w", err)
	}
	if contents.Version != TurnControlVersion {
		return TurnControlFileContents{}, fmt.Errorf("unsupported turn control version %d", contents.Version)
	}
	return contents, nil
}
