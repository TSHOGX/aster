package trajectory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// Capturer performs full-fidelity trajectory capture: it locates a session's
// native pi JSONL on the host, archives a durable copy into the research store,
// parses it into the normalized 7-primitive event stream, and persists the
// stream + a raw_sources pointer. It is the platform-side, multi-user analogue
// of the local sess-cli ingest.
type Capturer struct {
	store *store.Store
	// archiveRoot is <TrajectoryRoot>/raw; each session's native file is copied
	// to <archiveRoot>/<session_id>/<native-session-id>.jsonl so it survives any
	// future agent-home reclamation (the platform can't assume immortal homes
	// the way single-user sess-cli does).
	archiveRoot string
	log         *slog.Logger
}

// NewCapturer builds a Capturer. trajectoryRoot defaults are the caller's
// responsibility; an empty root disables archiving (capture becomes a no-op).
func NewCapturer(st *store.Store, trajectoryRoot string, log *slog.Logger) *Capturer {
	root := strings.TrimSpace(trajectoryRoot)
	if root == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &Capturer{store: st, archiveRoot: filepath.Join(root, "raw"), log: log}
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

// CaptureSession locates, archives, parses, and stores one session's native pi
// trajectory. It is idempotent: an unchanged native file (same size) is not
// re-archived, and the event stream is wholesale-replaced each run. Faux/test
// sessions are skipped. Sessions with no locatable native file are recorded at
// fidelity=partial (the distillation loop can still fall back to the DB
// projection via store.SnapshotSession).
func (c *Capturer) CaptureSession(ctx context.Context, rec store.PiSessionForCapture) (CaptureResult, error) {
	if c == nil {
		return CaptureResult{SessionID: rec.SessionID, Skipped: true, Reason: "capturer disabled"}, nil
	}
	nativePath, err := c.locateNativeFile(rec)
	if err != nil {
		return CaptureResult{SessionID: rec.SessionID, Fidelity: "partial", Reason: err.Error()}, nil
	}

	// Parse first (from the live native file) so we have meta for archiving/skip.
	f, err := os.Open(nativePath)
	if err != nil {
		return CaptureResult{SessionID: rec.SessionID, Fidelity: "partial", Reason: fmt.Sprintf("open native: %v", err)}, nil
	}
	events, meta, err := ParsePiSession(f)
	_ = f.Close()
	if err != nil {
		return CaptureResult{}, fmt.Errorf("parse native %q: %w", nativePath, err)
	}
	if meta.IsTest {
		return CaptureResult{SessionID: rec.SessionID, Skipped: true, Reason: "test session"}, nil
	}

	// Archive a durable copy.
	archivePath, size, mtime, err := c.archive(rec.SessionID, meta.NativeSessionID, nativePath)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("archive %q: %w", nativePath, err)
	}
	if err := c.store.UpsertRawSource(ctx, rec.SessionID, archivePath, "pi_jsonl", meta.NativeSessionID, size, mtime); err != nil {
		return CaptureResult{}, err
	}

	// Stamp the platform session id onto each event and persist.
	snap := store.TrajectorySnapshot{
		SessionID:    rec.SessionID,
		Status:       rec.Status,
		Agent:        "pi",
		UserID:       rec.UserID,
		StartedAt:    meta.StartedAt,
		EndedAt:      meta.EndedAt,
		TurnCount:    meta.TurnCount,
		MessageCount: len(events),
		Fingerprint:  meta.Fingerprint,
		Fidelity:     "full",
	}
	for i := range events {
		events[i].SessionID = rec.SessionID
		events[i].Idx = i
	}
	if err := c.store.ReplaceTrajectoryEvents(ctx, snap, events); err != nil {
		return CaptureResult{}, err
	}
	return CaptureResult{SessionID: rec.SessionID, Fidelity: "full", EventCount: len(events), ArchivePath: archivePath}, nil
}

// locateNativeFile resolves the on-host path of a session's native pi JSONL. It
// prefers the DB-recorded selected_session_relpath; when absent it globs the
// session dir and picks the most-recently-modified *.jsonl (pi may have written
// a file it never reported at a completed turn).
func (c *Capturer) locateNativeFile(rec store.PiSessionForCapture) (string, error) {
	home := strings.TrimSpace(rec.AgentHomeHostPath)
	if home == "" {
		return "", fmt.Errorf("no agent-home host path")
	}
	sessionsDir := filepath.Join(home, ".pi", "agent", "sessions")

	if rel := strings.TrimSpace(rec.SelectedSessionRelpath); rel != "" {
		candidate := filepath.Join(sessionsDir, filepath.FromSlash(rel))
		if info, err := os.Lstat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}

	// Glob fallback: newest *.jsonl anywhere under sessionsDir (1-deep like pi).
	var newest string
	var newestMod time.Time
	walkErr := filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable subtrees
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newest = path
		}
		return nil
	})
	if walkErr != nil || newest == "" {
		return "", fmt.Errorf("no native pi session file under %s", sessionsDir)
	}
	return newest, nil
}

// archive copies the native file into the research archive. It is idempotent:
// if the destination already exists with the same size, the copy is skipped.
// Returns the archive path, size, and source mtime.
func (c *Capturer) archive(sessionID, nativeSessionID, srcPath string) (string, int64, time.Time, error) {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	name := strings.TrimSpace(nativeSessionID)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(srcPath), ".jsonl")
	}
	dstDir := filepath.Join(c.archiveRoot, sessionID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", 0, time.Time{}, err
	}
	dstPath := filepath.Join(dstDir, sanitizeName(name)+".jsonl")

	if dstInfo, err := os.Stat(dstPath); err == nil && dstInfo.Size() == srcInfo.Size() {
		return dstPath, srcInfo.Size(), srcInfo.ModTime().UTC(), nil // unchanged
	}
	if err := copyFileAtomic(srcPath, dstPath); err != nil {
		return "", 0, time.Time{}, err
	}
	return dstPath, srcInfo.Size(), srcInfo.ModTime().UTC(), nil
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
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
	sessions, err := c.store.ListPiSessionsForCapture(ctx, since)
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
