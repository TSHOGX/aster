// Package manifest implements the immutable volume distribution format. It has
// no dependency on identity, persistence, host paths, or a runtime engine.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Format                    = "volume-manifest-v1"
	Profile                   = "authored-v1"
	SharedAssetsProfile       = "shared-assets-v1"
	MaxManifestBytes          = 16 << 20
	MaxFileBytes        int64 = 256 << 20
	MaxTreeBytes        int64 = 1 << 30
	MaxEntries                = 10000
	MaxPathBytes              = 512
	MaxDepth                  = 16
)

// Field order is part of the canonical wire encoding. Every field is required;
// directories carry an empty digest and size "0". Byte counts are decimal strings.
type Entry struct {
	Digest string `json:"digest"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Size   string `json:"size"`
}

type Descriptor struct {
	Capabilities []string `json:"capabilities"`
	Entries      []Entry  `json:"entries"`
	EntryCount   int      `json:"entry_count"`
	Format       string   `json:"format"`
	LibraryID    string   `json:"library_id"`
	Profile      string   `json:"profile"`
	PublisherID  string   `json:"publisher_id"`
	TotalBytes   string   `json:"total_bytes"`
	TreeDigest   string   `json:"tree_digest"`
	VersionID    string   `json:"version_id"`
}

// New sorts a copy of the entries, computes aggregates, and validates the tree.
// The default profile describes portable ordinary data.
func New(publisher, library, version string, entries []Entry) (Descriptor, error) {
	return NewProfile(publisher, library, version, Profile, entries)
}

func NewProfile(publisher, library, version, profile string, entries []Entry) (Descriptor, error) {
	if len(entries) == 0 && profile == SharedAssetsProfile {
		return Descriptor{}, Finding{Path: "AGENTS.md", Severity: "error", Message: "Add the required root UTF-8 text file."}
	}
	if len(entries) == 0 || len(entries) > MaxEntries {
		return Descriptor{}, fmt.Errorf("invalid entry count")
	}
	d := Descriptor{Capabilities: []string{"data", "workspace"}, Entries: append([]Entry(nil), entries...),
		EntryCount: len(entries), Format: Format, LibraryID: library, Profile: profile,
		PublisherID: publisher, VersionID: version}
	if profile == SharedAssetsProfile {
		d.Capabilities = []string{"shared_assets", "workspace"}
	}
	sort.Slice(d.Entries, func(i, j int) bool { return d.Entries[i].Path < d.Entries[j].Path })
	var total int64
	for _, e := range d.Entries {
		n, err := ParseSize(e.Size)
		if err != nil || n > MaxFileBytes || total > MaxTreeBytes-n {
			return Descriptor{}, fmt.Errorf("entry size exceeds profile")
		}
		total += n
	}
	d.TotalBytes = strconv.FormatInt(total, 10)
	d.TreeDigest = TreeDigest(d.Entries)
	return d, d.Validate()
}

func ParseSize(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != value {
		return 0, fmt.Errorf("noncanonical byte count")
	}
	return n, nil
}

func ValidDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

func ValidID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func ValidPath(value string, directory bool) error {
	if !utf8.ValidString(value) || value == "" || len(value) > MaxPathBytes || value != path.Clean(value) || strings.HasPrefix(value, "/") {
		return fmt.Errorf("path must be clean relative UTF-8")
	}
	parts := strings.Split(value, "/")
	if len(parts) > MaxDepth {
		return fmt.Errorf("path exceeds depth limit")
	}
	for i, part := range parts {
		if part == "." || part == ".." || strings.Contains(part, "\\") {
			return fmt.Errorf("invalid path component")
		}
		if (directory || i < len(parts)-1) && strings.TrimSpace(part) != part {
			return fmt.Errorf("directory component has edge whitespace")
		}
		for _, c := range part {
			if unicode.IsControl(c) {
				return fmt.Errorf("path contains control character")
			}
		}
	}
	return nil
}

func (d Descriptor) Validate() error {
	if d.Format != Format || !SupportedProfile(d.Profile) {
		return fmt.Errorf("unsupported manifest format or content profile")
	}
	if !ValidID(d.PublisherID) || !ValidID(d.LibraryID) || !ValidID(d.VersionID) {
		return fmt.Errorf("invalid immutable identity")
	}
	if len(d.Capabilities) != 2 || d.Capabilities[1] != "workspace" || d.Profile == Profile && d.Capabilities[0] != "data" || d.Profile == SharedAssetsProfile && d.Capabilities[0] != "shared_assets" {
		return fmt.Errorf("capabilities do not match content profile")
	}
	if len(d.Entries) == 0 || len(d.Entries) > MaxEntries || d.EntryCount != len(d.Entries) {
		return fmt.Errorf("invalid entry count")
	}
	parents := make(map[string]bool, len(d.Entries))
	var total int64
	for i, e := range d.Entries {
		if i > 0 && d.Entries[i-1].Path >= e.Path {
			return fmt.Errorf("duplicate or unordered paths")
		}
		if err := ValidPath(e.Path, e.Kind == "directory"); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
		if parent := path.Dir(e.Path); parent != "." && !parents[parent] {
			return fmt.Errorf("entry %d has no explicit directory parent", i)
		}
		n, err := ParseSize(e.Size)
		if err != nil || n > MaxFileBytes || total > MaxTreeBytes-n {
			return fmt.Errorf("entry %d exceeds size limit", i)
		}
		switch e.Kind {
		case "directory":
			if e.Digest != "" || n != 0 {
				return fmt.Errorf("directory carries file content")
			}
			parents[e.Path] = true
		case "file":
			if !ValidDigest(e.Digest) {
				return fmt.Errorf("invalid file digest")
			}
		default:
			return fmt.Errorf("unsupported entry kind")
		}
		total += n
	}
	n, err := ParseSize(d.TotalBytes)
	if err != nil || n != total {
		return fmt.Errorf("incorrect tree byte count")
	}
	if !ValidDigest(d.TreeDigest) || d.TreeDigest != TreeDigest(d.Entries) {
		return fmt.Errorf("incorrect tree digest")
	}
	if d.Profile == SharedAssetsProfile {
		for _, finding := range SharedAssetsFindings(d.Entries) {
			if finding.Severity == "error" {
				return finding
			}
		}
	}
	return nil
}

// TreeDigest retains the consumer's content identity: sorted kind-NUL-path-NUL-
// decimal size-NUL-file digest-LF records, with d/f kinds and no normalization.
func TreeDigest(entries []Entry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		kind := "f"
		if e.Kind == "directory" {
			kind = "d"
		}
		lines = append(lines, kind+"\x00"+e.Path+"\x00"+e.Size+"\x00"+e.Digest+"\n")
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, line := range lines {
		_, _ = io.WriteString(h, line)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (d Descriptor) Canonical() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds encoded byte limit")
	}
	return b, nil
}

// Decode accepts only the canonical representation. Comparing the original bytes
// rejects duplicate keys, missing fields, unknown fields, alternate encodings,
// invalid UTF-8, trailing values, and replacement-character JSON ambiguities.
func Decode(r io.Reader) (Descriptor, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return Descriptor{}, err
	}
	if len(b) > MaxManifestBytes {
		return Descriptor{}, fmt.Errorf("manifest exceeds encoded byte limit")
	}
	var d Descriptor
	if err := json.Unmarshal(b, &d); err != nil {
		return Descriptor{}, fmt.Errorf("invalid manifest JSON: %w", err)
	}
	canonical, err := d.Canonical()
	if err != nil {
		return Descriptor{}, err
	}
	if !bytes.Equal(b, canonical) {
		return Descriptor{}, fmt.Errorf("manifest is not canonical")
	}
	return d, nil
}

func (d Descriptor) Digest() (string, error) {
	b, err := d.Canonical()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = io.WriteString(h, Format+"\x00")
	_, _ = h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
