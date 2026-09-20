// Package content identifies an authored immutable file tree by its bytes.
//
// A revision digest is computed from the source tree alone: it names content, not
// an engine object, a host path or a publication attempt. That is what makes it
// usable as the idempotency key for publication and as the durable pointer the
// product compares a mounted volume against.
package content

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxEntries, MaxFileBytes and MaxTotalBytes bound an authored revision. Shared
// assets and agent configuration are reviewed text; anything larger is a packaging
// mistake, and an unbounded walk would let a source tree decide how much memory
// publication uses.
const (
	MaxEntries    = 4096
	MaxFileBytes  = 8 << 20
	MaxTotalBytes = 64 << 20
)

// Entry is one member of a revision. Path is slash-separated and relative to the
// revision root.
//
// There is no mode dimension. The engine's volume write verb takes no mode and its
// listing reports none, so an executable bit could neither be published nor
// verified; carrying one in the digest would assert something about published
// content that nothing checks. Read therefore refuses an executable source file
// instead of silently publishing it as non-executable.
//
// Source is the host path the bytes came from. It is publication input, never part
// of content identity: the same reviewed bytes must publish identically from any
// checkout.
type Entry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Source    string `json:"-"`
}

// Revision is an authored tree with its content identity.
type Revision struct {
	Digest     string
	Entries    []Entry
	TotalBytes int64
}

// Read walks root and computes the revision. Symlinks, devices, sockets and any
// other irregular entry are refused rather than followed: a published volume must
// mean exactly the bytes that were reviewed, and the guest launcher owns the one
// context symlink it needs inside its own overlay.
func Read(root string) (Revision, error) {
	var result Revision
	absolute, err := filepath.Abs(root)
	if err != nil {
		return result, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("authored content root must be a directory")
	}
	var entries []Entry
	walkErr := filepath.WalkDir(absolute, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == absolute {
			return nil
		}
		relative, err := filepath.Rel(absolute, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if err := ValidPath(relative); err != nil {
			return err
		}
		if len(entries) >= MaxEntries {
			return fmt.Errorf("authored content exceeds %d entries", MaxEntries)
		}
		switch mode := entry.Type(); {
		case mode.IsDir():
			entries = append(entries, Entry{Path: relative, Directory: true, Source: name})
			return nil
		case mode.IsRegular():
		default:
			return fmt.Errorf("authored content entry %q is not a regular file or directory", relative)
		}
		stat, err := entry.Info()
		if err != nil {
			return err
		}
		if stat.Mode().Perm()&0o111 != 0 {
			return fmt.Errorf("authored content file %q is executable, which publication cannot preserve", relative)
		}
		if stat.Size() > MaxFileBytes {
			return fmt.Errorf("authored content file %q exceeds %d bytes", relative, MaxFileBytes)
		}
		result.TotalBytes += stat.Size()
		if result.TotalBytes > MaxTotalBytes {
			return fmt.Errorf("authored content exceeds %d bytes", MaxTotalBytes)
		}
		digest, err := fileDigest(name, stat.Size())
		if err != nil {
			return err
		}
		entries = append(entries, Entry{Path: relative, Size: stat.Size(), Digest: digest, Source: name})
		return nil
	})
	if walkErr != nil {
		return Revision{}, walkErr
	}
	if len(entries) == 0 {
		return result, fmt.Errorf("authored content root is empty")
	}
	result.Entries = entries
	result.Digest = Digest(entries)
	return result, nil
}

// Digest is the canonical revision identity over a set of entries. Sorting here
// rather than trusting the caller's order keeps a directory walk, a published
// volume listing and a persisted pointer comparable without a shared traversal.
func Digest(entries []Entry) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		kind, digest := "f", entry.Digest
		size := fmt.Sprint(entry.Size)
		if entry.Directory {
			kind, digest, size = "d", "", "0"
		}
		lines = append(lines, fmt.Sprintf("%s\x00%s\x00%s\x00%s\n", kind, entry.Path, size, digest))
	}
	sort.Strings(lines)
	hash := sha256.New()
	for _, line := range lines {
		hash.Write([]byte(line))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// ValidDigest reports whether a string is a canonical revision digest. Publication
// keys and persisted pointers are checked with it before they reach the engine.
func ValidDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

// ValidPath constrains a revision-relative path to what both the engine's volume
// root and the product's own comparison can address unambiguously.
func ValidPath(value string) error {
	if value == "" || value != path.Clean(value) || strings.HasPrefix(value, "/") || len(value) > 512 {
		return fmt.Errorf("authored content path %q must be clean and relative", value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\x00\\") {
			return fmt.Errorf("authored content path %q must contain clean components", value)
		}
	}
	return nil
}

func contentDigest(data []byte) string {
	return "sha256:" + hex.EncodeToString(sha256Sum(data))
}
func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func fileDigest(name string, size int64) (string, error) {
	file, err := os.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	// The stat that sized the read and the stat behind the digest must describe one
	// file: a source tree changing underneath publication is a refusal, not a digest.
	if !info.Mode().IsRegular() || info.Size() != size {
		return "", fmt.Errorf("authored content file %q changed during read", name)
	}
	hash := sha256.New()
	copied, err := io.Copy(hash, io.LimitReader(file, size+1))
	if err != nil {
		return "", err
	}
	if copied != size {
		return "", fmt.Errorf("authored content file %q changed during read", name)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
