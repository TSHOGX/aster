package authoring

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"aster/contracts/volumemanifest"
)

type Validation struct {
	Profile          string             `json:"profile"`
	Revision         int64              `json:"revision"`
	Valid            bool               `json:"valid"`
	Findings         []manifest.Finding `json:"findings"`
	SkillCount       int                `json:"skill_count"`
	AgentsBytes      string             `json:"agents_bytes"`
	AgentsPreview    string             `json:"agents_preview"`
	PreviewTruncated bool               `json:"preview_truncated"`
}

func (s *Store) Validation(ctx context.Context, library, version string) (Validation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Library(ctx, library)
	if err != nil {
		return Validation{}, err
	}
	out := Validation{Profile: l.Profile, Revision: l.DraftRevision, Findings: []manifest.Finding{}, Valid: true}
	var tree []manifest.Entry
	if version == "" {
		tree, err = entries(ctx, s.db, "draft_entries", "library_id", library)
	} else {
		v, e := s.Version(ctx, version)
		if e != nil {
			return out, e
		}
		if v.LibraryID != library || v.State != "published" && v.State != "archived" {
			return out, ErrNotFound
		}
		out.Profile, out.Revision = v.Profile, v.SourceRevision
		tree, err = entries(ctx, s.db, "version_entries", "version_id", version)
	}
	if err != nil {
		return out, err
	}
	if out.Profile != manifest.SharedAssetsProfile {
		return out, nil
	}
	out.Findings = manifest.SharedAssetsFindings(tree)
	for _, e := range tree {
		if strings.HasPrefix(e.Path, "skills/") && strings.Count(e.Path, "/") == 1 && e.Kind == "directory" {
			out.SkillCount++
		}
		if e.Path != "AGENTS.md" || e.Kind != "file" {
			continue
		}
		out.AgentsBytes = e.Size
		f, err := os.Open(s.blobPath(e.Digest))
		if err != nil {
			return out, err
		}
		raw, err := io.ReadAll(io.LimitReader(f, manifest.SharedAssetsMaxFileBytes+1))
		err = errors.Join(err, f.Close())
		if err != nil {
			return out, err
		}
		if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
			out.Findings = append(out.Findings, manifest.Finding{Path: e.Path, Severity: "error", Message: "Use UTF-8 text without NUL bytes."})
			continue
		}
		if len(raw) > 16384 {
			out.PreviewTruncated = true
			raw = raw[:16384]
			for !utf8.Valid(raw) {
				raw = raw[:len(raw)-1]
			}
		}
		out.AgentsPreview = string(raw)
	}
	for _, f := range out.Findings {
		if f.Severity == "error" {
			out.Valid = false
		}
	}
	return out, nil
}
