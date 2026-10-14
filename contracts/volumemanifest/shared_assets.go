package manifest

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const SharedAssetsMaxEntries = 4096
const SharedAssetsMaxFileBytes int64 = 8 << 20
const SharedAssetsMaxTreeBytes int64 = 64 << 20

func SupportedProfile(profile string) bool {
	return profile == Profile || profile == SharedAssetsProfile
}

type Finding struct {
	Path     string `json:"path"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (f Finding) Error() string { return f.Path + ": " + f.Message }

// SharedAssetsFindings checks profile rules independently of draft completeness.
// Drafts can retain errors; immutable publication and import cannot.
func SharedAssetsFindings(entries []Entry) []Finding {
	findings := []Finding{}
	add := func(path, severity, message string) { findings = append(findings, Finding{path, severity, message}) }
	byPath := map[string]Entry{}
	var total int64
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if e, ok := byPath["AGENTS.md"]; !ok || e.Kind != "file" {
		add("AGENTS.md", "error", "Add the required root UTF-8 text file.")
	}
	if e, ok := byPath["skills"]; ok && e.Kind != "directory" {
		add("skills", "error", "Use a directory for skills.")
	}
	if len(entries) > SharedAssetsMaxEntries {
		add(".", "error", "Shared assets exceed the 4,096 entry limit.")
	}
	for _, e := range entries {
		n, err := ParseSize(e.Size)
		if err != nil {
			add(e.Path, "error", "Invalid byte count.")
			continue
		}
		if n > SharedAssetsMaxFileBytes {
			add(e.Path, "error", "Shared-assets files must not exceed 8 MiB.")
		}
		if n > SharedAssetsMaxTreeBytes-total {
			add(e.Path, "error", "Shared assets exceed the 64 MiB tree limit.")
		} else {
			total += n
		}
		parts := strings.Split(e.Path, "/")
		for _, part := range parts {
			lower := strings.ToLower(part)
			if lower == ".env" || strings.HasPrefix(lower, ".env.") || lower == ".ssh" || lower == ".aws" || lower == ".git" || lower == "credentials.json" || lower == "auth.json" || lower == "runtime" || lower == "sessions" || lower == "agent-config" {
				add(e.Path, "error", "Remove runtime state, configuration, or secret paths from shared assets.")
				break
			}
		}
		if len(parts) == 2 && parts[0] == "skills" {
			if e.Kind != "directory" {
				add(e.Path, "error", "Place each skill in its own directory.")
			} else if skill, ok := byPath[e.Path+"/SKILL.md"]; !ok || skill.Kind != "file" {
				add(e.Path+"/SKILL.md", "error", "Add the skill's SKILL.md file.")
			}
		}
		if e.Kind == "file" && e.Path != "AGENTS.md" && !strings.HasPrefix(e.Path, "skills/") {
			add(e.Path, "warning", "Reference material is available read-only to the Agent.")
		}
	}
	return findings
}

// ValidateContent checks bytes whose meaning cannot be established from metadata.
// Call before any external engine allocation and again before publication.
func (d Descriptor) ValidateContent(open func(string) (io.ReadCloser, error)) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Profile != SharedAssetsProfile {
		return nil
	}
	for _, e := range d.Entries {
		if e.Path != "AGENTS.md" {
			continue
		}
		file, err := open(e.Path)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(file, SharedAssetsMaxFileBytes+1))
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
			return Finding{e.Path, "error", "Use UTF-8 text without NUL bytes."}
		}
		size, _ := ParseSize(e.Size)
		if int64(len(raw)) != size || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != e.Digest {
			return Finding{e.Path, "error", "Content does not match its immutable digest."}
		}
	}
	return nil
}
