package artifacts

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/pathutil"
	"harness-platform/orchestrator/internal/store"
)

type Watcher struct {
	volumeConfig store.DataVolumeProvisionerConfig
	store        *store.Store
	hub          *events.Hub
	log          *slog.Logger

	// workspaceCache holds the most recent verified-workspace snapshot so that
	// per-filesystem-event path resolution does not hit the DB on every event.
	// It is refreshed by the 1-second ticker via refreshWorkspaceWatches.
	mu                  sync.Mutex
	workspaceCache      []store.SessionWorkspaceVolume
	workspaceCacheReady bool
}

func New(volumeConfig store.DataVolumeProvisionerConfig, store *store.Store, hub *events.Hub, log *slog.Logger) *Watcher {
	return &Watcher{volumeConfig: volumeConfig, store: store, hub: hub, log: log}
}

func (w *Watcher) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	watchedRoots := map[string]struct{}{}
	if err := w.refreshWorkspaceWatches(ctx, watcher, watchedRoots); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.refreshWorkspaceWatches(ctx, watcher, watchedRoots); err != nil {
				w.log.Warn("failed to refresh artifact workspace watches", "error", err)
			}
		case err := <-watcher.Errors:
			if err != nil {
				w.log.Warn("artifact watcher error", "error", err)
			}
		case event := <-watcher.Events:
			if event.Has(fsnotify.Create) {
				if info, err := os.Lstat(event.Name); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					if _, _, _, ok := w.resolveArtifactPath(ctx, event.Name); ok {
						_ = w.addRecursive(watcher, event.Name)
					}
					continue
				}
			}
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				w.recordPath(ctx, event.Name)
			}
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				w.deletePath(ctx, event.Name)
			}
		}
	}
}

func (w *Watcher) ScanSession(ctx context.Context, sessionID string) error {
	workspace, err := w.store.VerifySessionWorkspaceVolume(ctx, store.VerifySessionWorkspaceVolumeParams{
		SessionID: sessionID,
		Config:    w.volumeConfig,
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(workspace.HostPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		artifactPath, ok := pathutil.Rel(workspace.HostPath, path)
		if !ok {
			return nil
		}
		w.recordWorkspacePath(ctx, workspace.SessionID, workspace.HostPath, artifactPath, path)
		return nil
	})
}

func (w *Watcher) refreshWorkspaceWatches(ctx context.Context, watcher *fsnotify.Watcher, watchedRoots map[string]struct{}) error {
	workspaces, err := w.store.ListVerifiedSessionWorkspaceVolumes(ctx, w.volumeConfig)
	if err != nil {
		return err
	}
	// Publish the snapshot so per-event path resolution can read it instead of
	// querying the DB on every filesystem event.
	w.setWorkspaceCache(workspaces)

	validRoots := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		validRoots[workspace.HostPath] = struct{}{}
		if _, ok := watchedRoots[workspace.HostPath]; ok {
			continue
		}
		info, err := os.Lstat(workspace.HostPath)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return os.ErrInvalid
		}
		if err := w.addRecursive(watcher, workspace.HostPath); err != nil {
			return err
		}
		watchedRoots[workspace.HostPath] = struct{}{}
	}

	// Prune roots (and their descendant directory watches) for workspaces that
	// are no longer present, so watchedRoots and the underlying watch set do not
	// grow unbounded over the process lifetime.
	for root := range watchedRoots {
		if _, ok := validRoots[root]; ok {
			continue
		}
		w.removeRecursive(watcher, root)
		delete(watchedRoots, root)
	}
	return nil
}

// setWorkspaceCache stores the latest verified-workspace snapshot. Guarded by a
// mutex: within Run the ticker (writer) and events (reader) share one goroutine,
// but the lock keeps the cache safe if that ever changes and is negligible cost.
func (w *Watcher) setWorkspaceCache(workspaces []store.SessionWorkspaceVolume) {
	w.mu.Lock()
	w.workspaceCache = workspaces
	w.workspaceCacheReady = true
	w.mu.Unlock()
}

// workspaceSnapshot returns the cached verified-workspace list and whether the
// cache has been populated yet.
func (w *Watcher) workspaceSnapshot() ([]store.SessionWorkspaceVolume, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.workspaceCache, w.workspaceCacheReady
}

// removeRecursive drops the watch for root and any descendant directory watches
// this watcher registered under it.
func (w *Watcher) removeRecursive(watcher *fsnotify.Watcher, root string) {
	for _, watched := range watcher.WatchList() {
		if watched == root || pathutil.Within(root, watched) {
			if err := watcher.Remove(watched); err != nil {
				w.log.Warn("failed to remove artifact directory watch", "path", watched, "error", err)
			}
		}
	}
}

func (w *Watcher) addRecursive(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			w.log.Warn("failed to watch artifact directory", "path", path, "error", err)
		}
		return nil
	})
}

func (w *Watcher) recordPath(ctx context.Context, path string) {
	sessionID, artifactPath, workspaceRoot, ok := w.resolveArtifactPath(ctx, path)
	if !ok {
		return
	}
	w.recordWorkspacePath(ctx, sessionID, workspaceRoot, artifactPath, path)
}

func (w *Watcher) recordWorkspacePath(ctx context.Context, sessionID, workspaceRoot, artifactPath, path string) {
	info, err := os.Lstat(path)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	if hasSymlinkComponent(workspaceRoot, artifactPath) {
		return
	}
	realWorkspaceRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !pathutil.Within(realWorkspaceRoot, realPath) {
		return
	}
	artifact := store.Artifact{
		SessionID: sessionID,
		Path:      artifactPath,
		Size:      info.Size(),
		ModTime:   info.ModTime().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := w.store.UpsertArtifact(ctx, artifact); err != nil {
		w.log.Warn("failed to record artifact", "path", path, "error", err)
		return
	}
	w.hub.Publish(events.Event{Type: "artifact.updated", SessionID: artifact.SessionID, Payload: artifact})
}

func (w *Watcher) deletePath(ctx context.Context, path string) {
	sessionID, artifactPath, _, ok := w.resolveArtifactPath(ctx, path)
	if !ok {
		return
	}
	if err := w.store.DeleteArtifactPath(ctx, sessionID, artifactPath); err != nil {
		w.log.Warn("failed to delete artifact metadata", "path", path, "error", err)
		return
	}
	w.hub.Publish(events.Event{
		Type:      "artifact.deleted",
		SessionID: sessionID,
		Payload: map[string]string{
			"session_id": sessionID,
			"path":       artifactPath,
		},
	})
}

func (w *Watcher) resolveArtifactPath(ctx context.Context, path string) (string, string, string, bool) {
	workspaces, ok := w.workspaceSnapshot()
	if !ok {
		// The cache has not been populated yet (e.g. resolution happening
		// outside the Run loop). Fall back to a direct query so behavior is
		// unchanged in that case.
		var err error
		workspaces, err = w.store.ListVerifiedSessionWorkspaceVolumes(ctx, w.volumeConfig)
		if err != nil {
			w.log.Warn("failed to resolve artifact workspace", "path", path, "error", err)
			return "", "", "", false
		}
	}
	for _, workspace := range workspaces {
		artifactPath, ok := pathutil.Rel(workspace.HostPath, path)
		if ok {
			return workspace.SessionID, artifactPath, workspace.HostPath, true
		}
	}
	return "", "", "", false
}

func hasSymlinkComponent(root, slashPath string) bool {
	current := root
	for _, segment := range strings.Split(slashPath, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return true
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

