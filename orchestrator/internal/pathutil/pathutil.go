// Package pathutil provides lexical filepath containment helpers shared across
// the orchestrator. These functions operate purely on the textual form of the
// paths (via filepath.Rel, which cleans its arguments internally); they never
// touch the filesystem and never resolve symlinks. Callers that need symlink
// resolution must EvalSymlinks first and pass the resolved paths in.
package pathutil

import (
	"path/filepath"
	"strings"
)

// Within reports whether path is the root itself or a descendant of root.
//
// The comparison is lexical: filepath.Rel cleans both arguments, so passing
// already-cleaned or absolute paths is optional and does not change the result.
// An equal path (root == path) is considered within.
func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Rel returns the slash-normalized relative path from root to path when path is
// a strict descendant of root. Unlike Within, an equal path (rel == ".") is
// rejected: ok is false for the root itself, for parents, and for any sibling
// that would require ".." to reach. Both arguments are cleaned before the
// comparison.
func Rel(root, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
