package trajectory

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
)

// Capturer archives full native Pi bytes before parsing that same snapshot into
// the existing consumer event schema. The source may be local or engine-backed;
// downstream memory consumers receive archive references, never engine clients.
type Capturer struct {
	// store is the MEMORY database: every write this type performs
	// (raw_sources, trajectory_events, trajectory_snapshots) lands there.
	store *memorystore.Store
	// ingest is the control -> memory boundary, used for exactly one thing: the
	// session enumeration CaptureAll walks. The Capturer holds no control-store
	// handle, so it cannot write control-plane data even by accident.
	ingest *memorystore.Ingestor
	// archiveRoot is <TrajectoryRoot>/raw; each session's native file is copied
	// to <archiveRoot>/<session_id>/<native-session-id>.jsonl so it survives any
	// future agent-home reclamation (the platform can't assume immortal homes
	// the way single-user sess-cli does).
	archiveRoot string
	log         *slog.Logger
}

// NewCapturer builds a Capturer over the memory store, with ingest supplying the
// control-plane session enumeration. trajectoryRoot defaults are the caller's
// responsibility; an empty root disables archiving (capture becomes a no-op).
func NewCapturer(st *memorystore.Store, ingest *memorystore.Ingestor, trajectoryRoot string, log *slog.Logger) *Capturer {
	root := strings.TrimSpace(trajectoryRoot)
	if root == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &Capturer{store: st, ingest: ingest, archiveRoot: filepath.Join(root, "raw"), log: log}
}

// CaptureResult reports what one CaptureSession did.
type CaptureResult struct {
	SessionID   string
	Fidelity    string // full | partial | skipped
	EventCount  int
	ArchivePath string
	Skipped     bool
	Reason      string
}

// CaptureSession adapts existing local session enumeration to stream capture.
// Missing local files retain the existing partial-fidelity result; immutable
// archive identity is based on content, not source file size.
func (c *Capturer) CaptureSession(ctx context.Context, rec memorystore.PiSessionForCapture) (CaptureResult, error) {
	if c == nil {
		return CaptureResult{SessionID: rec.SessionID, Skipped: true, Reason: "capturer disabled"}, nil
	}
	source, modified, err := c.openLocalNative(ctx, rec)
	if err != nil {
		return CaptureResult{SessionID: rec.SessionID, Fidelity: "partial", Reason: err.Error()}, nil
	}
	defer source.Close()
	return c.CaptureStream(ctx, CaptureIdentity{SessionID: rec.SessionID, UserID: rec.UserID, Status: rec.Status, NativeSessionID: rec.NativeSessionID}, source, modified)
}

// sanitizeName keeps a filename safe (native session ids are uuids, but be safe).
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}
	out := strings.Map(repl, s)
	if out == "" {
		return "session"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// CaptureAll enumerates and captures every pi session in the window (zero since =
// all). Returns per-session results; individual failures are logged and do not
// abort the batch. Used by both the nightly sweep and the backfill command.
func (c *Capturer) CaptureAll(ctx context.Context, since time.Time) ([]CaptureResult, error) {
	if c == nil {
		return nil, nil
	}
	sessions, err := c.ingest.ListPiSessionsForCapture(ctx, since)
	if err != nil {
		return nil, err
	}
	results := make([]CaptureResult, 0, len(sessions))
	for _, rec := range sessions {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		res, err := c.CaptureSession(ctx, rec)
		if err != nil {
			c.log.Warn("trajectory capture failed", "session_id", rec.SessionID, "error", err)
			continue
		}
		results = append(results, res)
	}
	// Stable order for deterministic reporting.
	sort.Slice(results, func(i, j int) bool { return results[i].SessionID < results[j].SessionID })
	return results, nil
}
