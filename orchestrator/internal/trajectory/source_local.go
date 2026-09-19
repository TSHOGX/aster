package trajectory

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/local"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (c *Capturer) openLocalNative(ctx context.Context, rec memorystore.PiSessionForCapture) (io.ReadCloser, time.Time, error) {
	path, err := c.locateNativeFile(rec)
	if err != nil {
		return nil, time.Time{}, err
	}
	relative, err := filepath.Rel(rec.AgentHomeHostPath, path)
	if err != nil {
		return nil, time.Time{}, err
	}
	reader, metadata, err := workspace.Open(ctx, local.Directory{Path: rec.AgentHomeHostPath}, filepath.ToSlash(relative))
	return reader, metadata.ModifiedAt, err
}

// locateNativeFile resolves the on-host path of a session's native pi JSONL. It
// prefers the DB-recorded selected_session_relpath; when absent it globs the
// session dir and picks the most-recently-modified *.jsonl (pi may have written
// a file it never reported at a completed turn).
func (c *Capturer) locateNativeFile(rec memorystore.PiSessionForCapture) (string, error) {
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
