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
	if !manifest.ValidID(version) {
		return Validation{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.Version(ctx, version)
	if err != nil {
		return Validation{}, err
	}
	if v.LibraryID != library || (v.State != "published" && v.State != "archived") {
		return Validation{}, ErrNotFound
	}
	tree, err := entries(ctx, s.db, "version_entries", "version_id", version)
	if err != nil {
		return Validation{}, err
	}
	return s.validateTree(Validation{Profile: v.Profile, Revision: v.SourceRevision, Findings: []manifest.Finding{}, Valid: true}, tree)
}
func (s *Store) validateTree(out Validation, tree []manifest.Entry) (Validation, error) {
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

func (s *Store) BranchValidation(ctx context.Context, a Actor, library, branch string, revision int64) (Validation, error) {
	if err := s.authorizeLibrary(ctx, a, library, "volume.read"); err != nil {
		return Validation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.branch(ctx, library, branch)
	if err != nil {
		return Validation{}, err
	}
	if b.DraftRevision != revision {
		return Validation{}, ErrConflict
	}
	l, err := s.Library(ctx, library)
	if err != nil {
		return Validation{}, err
	}
	tree, err := entries(ctx, s.db, "branch_entries", "branch_id", branch)
	if err != nil {
		return Validation{}, err
	}
	return s.validateTree(Validation{Profile: l.Profile, Revision: b.DraftRevision, Findings: []manifest.Finding{}, Valid: true}, tree)
}
