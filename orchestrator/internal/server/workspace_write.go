package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"syscall"

	"harness-platform/orchestrator/internal/store"
)

// workspaceWriteError carries an HTTP status alongside a message so the caller
// can translate a failed workspace write into the right client response. It
// mirrors the (status, message) return shape of openArtifactFile.
type workspaceWriteError struct {
	status  int
	message string
}

func (e *workspaceWriteError) Error() string { return e.message }

func newWorkspaceWriteError(status int, message string) *workspaceWriteError {
	return &workspaceWriteError{status: status, message: message}
}

// writeWorkspaceStatus extracts the HTTP status and message from an error
// returned by writeWorkspaceFile, defaulting to 500 for unexpected errors.
func writeWorkspaceStatus(err error) (int, string) {
	var we *workspaceWriteError
	if errors.As(err, &we) {
		return we.status, we.message
	}
	return http.StatusInternalServerError, err.Error()
}

// writeWorkspaceFile writes content into a session's workspace at relPath,
// creating parent directories as needed and giving every file/dir it creates
// the sandbox uid/gid so the agent (running as nobody) can read its own
// uploads. relPath is hardened byte-for-byte like openArtifactFile: the single
// highest-risk point here is path traversal, which would turn an upload into an
// arbitrary host-file write, so we reject `..`, backslashes, and symlinked
// components on both the destination and every parent directory.
//
// The write is atomic: content lands in a temp file in the destination dir,
// is fsync'd, chmod'd 0o640, chowned to the sandbox identity, then renamed over
// the target. It returns the cleaned slash-relative path actually written.
func (s *Server) writeWorkspaceFile(ctx context.Context, sessionID, relPath string, content io.Reader, maxBytes int64) (string, error) {
	cleanPath, err := sanitizeWorkspaceRelPath(relPath)
	if err != nil {
		return "", err
	}

	volumeConfig, cfgErr := s.dataVolumeProvisionerConfig()
	if cfgErr != nil {
		return "", cfgErr
	}
	workspace, verifyErr := s.store.VerifySessionWorkspaceVolume(ctx, store.VerifySessionWorkspaceVolumeParams{
		SessionID: sessionID,
		Config:    volumeConfig,
	})
	if errors.Is(verifyErr, sql.ErrNoRows) {
		return "", newWorkspaceWriteError(http.StatusNotFound, "session not found")
	}
	if verifyErr != nil {
		return "", newWorkspaceWriteError(http.StatusForbidden, "workspace evidence invalid")
	}

	sessionRoot := workspace.HostPath
	uid := workspace.SandboxUID
	gid := workspace.SandboxGID

	fullPath := filepath.Join(sessionRoot, filepath.FromSlash(cleanPath))
	if !isPathInside(sessionRoot, fullPath) {
		return "", newWorkspaceWriteError(http.StatusBadRequest, "invalid artifact path")
	}

	realSessionRoot, err := filepath.EvalSymlinks(sessionRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", newWorkspaceWriteError(http.StatusNotFound, "session not found")
		}
		return "", err
	}

	// Reject symlinked components among the parents that already exist. Unlike
	// the read path we cannot require the full path to pre-exist (we may be
	// creating new dirs and the file), so we validate each existing ancestor and
	// stop at the first missing one, which MkdirAll will then create fresh.
	if err := rejectSymlinkParents(sessionRoot, cleanPath); err != nil {
		return "", err
	}

	parentDir := filepath.Dir(fullPath)
	if err := s.ensureWorkspaceDirs(sessionRoot, cleanPath, uid, gid); err != nil {
		return "", err
	}

	// Re-validate the resolved parent after creation to defeat a symlink swapped
	// in concurrently: the real parent must still live inside the real root.
	realParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		return "", err
	}
	if !isPathInside(realSessionRoot, realParent) {
		return "", newWorkspaceWriteError(http.StatusForbidden, "artifact path escapes session workspace")
	}

	// If the destination already exists it must be a regular file (never a
	// symlink or directory) so we overwrite content, not a link target.
	if info, statErr := os.Lstat(fullPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", newWorkspaceWriteError(http.StatusForbidden, "artifact path uses a symlink")
		}
		if !info.Mode().IsRegular() {
			return "", newWorkspaceWriteError(http.StatusForbidden, "artifact is not a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}

	if err := s.atomicWriteOwned(parentDir, fullPath, content, maxBytes, uid, gid); err != nil {
		return "", err
	}
	return cleanPath, nil
}

// sanitizeWorkspaceRelPath applies the exact segment/traversal hardening
// openArtifactFile uses for the artifact path portion and returns the cleaned
// slash path.
func sanitizeWorkspaceRelPath(relPath string) (string, error) {
	if relPath == "" || strings.Contains(relPath, `\`) {
		return "", newWorkspaceWriteError(http.StatusBadRequest, "invalid artifact path")
	}
	for _, segment := range strings.Split(relPath, "/") {
		if !safePathSegment(segment) {
			return "", newWorkspaceWriteError(http.StatusBadRequest, "invalid artifact path")
		}
	}
	cleanPath := pathpkg.Clean(relPath)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return "", newWorkspaceWriteError(http.StatusBadRequest, "invalid artifact path")
	}
	return cleanPath, nil
}

// rejectSymlinkParents walks the ancestor directories of cleanPath under root
// and rejects any that is a symlink. It stops at the first component that does
// not yet exist, since those will be created fresh by ensureWorkspaceDirs.
func rejectSymlinkParents(root, cleanPath string) error {
	segments := strings.Split(cleanPath, "/")
	current := root
	// Only the ancestors (all but the final file segment) matter here.
	for _, segment := range segments[:len(segments)-1] {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return newWorkspaceWriteError(http.StatusForbidden, "artifact path uses a symlink")
		}
		if !info.IsDir() {
			return newWorkspaceWriteError(http.StatusForbidden, "artifact parent is not a directory")
		}
	}
	return nil
}

// ensureWorkspaceDirs creates each parent directory of cleanPath that does not
// yet exist, chowning newly created dirs to the sandbox identity (0o750) so the
// agent can traverse into folders holding its uploads.
func (s *Server) ensureWorkspaceDirs(root, cleanPath string, uid, gid int) error {
	segments := strings.Split(cleanPath, "/")
	current := root
	for _, segment := range segments[:len(segments)-1] {
		current = filepath.Join(current, segment)
		if info, err := os.Lstat(current); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return newWorkspaceWriteError(http.StatusForbidden, "artifact path uses a symlink")
			}
			if !info.IsDir() {
				return newWorkspaceWriteError(http.StatusForbidden, "artifact parent is not a directory")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(current, 0o750); err != nil && !os.IsExist(err) {
			return err
		}
		if err := chownToSandbox(current, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(current, 0o750); err != nil {
			return err
		}
	}
	return nil
}

// atomicWriteOwned streams content (bounded to maxBytes) into a temp file in
// dir, fsyncs, sets mode 0o640, chowns to the sandbox identity, and renames it
// over dstPath. A partially written temp file is removed on any failure.
func (s *Server) atomicWriteOwned(dir, dstPath string, content io.Reader, maxBytes int64, uid, gid int) error {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	// Bound the copy so a lying Content-Length or chunked body cannot exceed the
	// caller's cap. We allow maxBytes bytes and treat one extra byte as overflow.
	limited := io.LimitReader(content, maxBytes+1)
	written, err := io.Copy(tmp, limited)
	if err != nil {
		cleanup()
		return err
	}
	if written > maxBytes {
		cleanup()
		return newWorkspaceWriteError(http.StatusRequestEntityTooLarge, "file exceeds maximum allowed size")
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := chownToSandbox(tmpName, uid, gid); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// chownToSandbox chowns path to uid/gid, mirroring ensureSandboxOwnership in
// the runtime package: chown requires euid 0, but when the process is not root
// and the file already carries the desired ownership we treat it as a no-op so
// non-root test/dev environments still work.
func chownToSandbox(path string, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("stat ownership unavailable for %s", path)
	}
	if int(stat.Uid) == uid && int(stat.Gid) == gid {
		return nil
	}
	if os.Geteuid() != 0 {
		// Not root and ownership differs: leave the file as-is rather than
		// failing. The workspace dir is owned by the invoking user in dev, and
		// production always runs the orchestrator as root.
		return nil
	}
	return os.Chown(path, uid, gid)
}
