package memory

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
)

// Promotion thresholds (from decisions/trajectory-pipeline.md §Step-4): a memory
// or skill enters the shared KB only when it is repeated, confident, and
// uncontradicted. Memory can evolve fast; skills evolve slowly.
const (
	minFactSupport       = 2
	minSkillSupport      = 3
	promoteSupport       = 3
	promoteConfidence    = 0.75
	promoteMaxContradict = 0
	// inUseSupport is the higher bar at which an already-published skill flips
	// from `observing` to `in-use`.
	inUseSupport    = 5
	inUseConfidence = 0.85
)

// Report summarizes one sweep for the daily markdown report.
type Report struct {
	GeneratedAt      time.Time
	SessionsSwept    int
	AcceptedMemories int
	SkillCandidates  int
	PublishedSkills  int
	ObservingSkills  int
	InUseSkills      int
	Digest           string
	Notes            []string
}

// curateAndPublish consolidates accepted knowledge and, if anything qualifies,
// writes a fresh KB snapshot and atomically re-points `current`. It is
// idempotent: re-running with no new qualifying knowledge is a no-op that leaves
// `current` unchanged.
func (p *Pipeline) curateAndPublish(ctx context.Context) (Report, error) {
	report := Report{}

	// 1. Accept semantic memories that clear the bar (candidate → accepted).
	candidates, err := p.store.ListSemanticMemories(ctx, "candidate")
	if err != nil {
		return report, fmt.Errorf("list candidate memories: %w", err)
	}
	for _, m := range candidates {
		if distinctEvidenceCount(m.EvidenceSessionIDs) >= minFactSupport && qualifies(m.SupportCount, m.Confidence, m.ContradictionCount) {
			m.Status = "accepted"
			if _, err := p.store.UpsertSemanticMemory(ctx, m, ""); err != nil {
				p.log.Warn("curator: accept memory failed", "id", m.ID, "error", err)
				continue
			}
			report.AcceptedMemories++
		}
	}

	// 2. Decide the KB skill set: accepted skill candidates that clear the bar.
	skills, err := p.store.ListSkillCandidates(ctx, "")
	if err != nil {
		return report, fmt.Errorf("list skill candidates: %w", err)
	}
	accepted, err := p.store.ListSemanticMemories(ctx, "accepted")
	if err != nil {
		return report, fmt.Errorf("list accepted memories: %w", err)
	}

	var publish []memorystore.SkillCandidate
	for _, sk := range skills {
		if sk.Status == "rejected" {
			continue
		}
		if distinctEvidenceCount(sk.EvidenceSessionIDs) < minSkillSupport {
			report.SkillCandidates++
			continue
		}
		if !qualifies(sk.SupportCount, sk.Confidence, 0) {
			report.SkillCandidates++
			continue
		}
		// Flip observing → in-use once it has sustained support.
		kbStatus := "observing"
		if sk.SupportCount >= inUseSupport && sk.Confidence >= inUseConfidence {
			kbStatus = "in-use"
		}
		sk.KBStatus = kbStatus
		publish = append(publish, sk)
		if kbStatus == "in-use" {
			report.InUseSkills++
		} else {
			report.ObservingSkills++
		}
	}

	// 3. Nothing to publish and no KB configured → done.
	if p.kb == nil {
		report.GeneratedAt = time.Now().UTC()
		return report, nil
	}
	if len(publish) == 0 && len(accepted) == 0 {
		report.GeneratedAt = time.Now().UTC()
		return report, nil
	}

	// 4. Write a new snapshot (facts from accepted memories + skills). With
	// autoPublish it goes live immediately; by default it is only staged and an
	// expert must approve it before any sandbox mounts it. Same gate as the
	// agentic path — no back door.
	var digest string
	if p.autoPublish {
		d, err := p.kb.Publish(accepted, publish)
		if err != nil {
			return report, fmt.Errorf("publish kb snapshot: %w", err)
		}
		digest = d
	} else {
		d, changed, err := p.kb.Stage(accepted, publish, 0, nil)
		if err != nil {
			return report, fmt.Errorf("stage kb snapshot: %w", err)
		}
		digest = d
		if changed {
			report.Notes = append(report.Notes,
				"STAGED awaiting expert approval — not live. Review: harness-ctl kb diff / approve "+digest)
			p.log.Info("curator: snapshot STAGED, awaiting expert approval", "digest", digest)
		}
	}
	report.Digest = digest

	// 5. Mark published skills.
	for _, sk := range publish {
		if err := p.store.MarkSkillCandidatePublished(ctx, sk.ID, sk.KBStatus); err != nil {
			p.log.Warn("curator: mark published failed", "id", sk.ID, "error", err)
			continue
		}
		report.PublishedSkills++
	}
	report.GeneratedAt = time.Now().UTC()
	return report, nil
}

func qualifies(support int, confidence float64, contradictions int) bool {
	return support >= promoteSupport && confidence >= promoteConfidence && contradictions <= promoteMaxContradict
}

// ---- small normalization/formatting helpers shared by the pipeline ----

func normalizeOutcome(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "success", "partial", "failed", "abandoned":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "unknown"
	}
}

func normalizeFrustration(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "mild", "high":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "unknown"
	}
}

func normalizeMemType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fact", "heuristic", "procedure", "bug", "preference":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "fact"
	}
}

func clampConfidence(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	if c == 0 {
		return 0.5 // model omitted it; assume neutral
	}
	return c
}

func defaultString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a skill title into a filesystem-safe, stable skill name. Falls
// back to a hash-free scheme; non-ASCII (e.g. Chinese titles) collapse to empty
// segments, so we keep alphanumerics and, if nothing survives, use a length tag.
func slugify(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	slug := strings.Trim(slugRe.ReplaceAllString(lower, "-"), "-")
	if slug == "" {
		return fmt.Sprintf("skill-%d", len([]rune(s)))
	}
	if len(slug) > 64 {
		slug = slug[:64]
	}
	return slug
}

// renderSkillMarkdown produces the human-reviewable SKILL.md body for a
// candidate skill, with the frontmatter status key the KB lifecycle uses.
func renderSkillMarkdown(sk CandidateSkill, sessionID string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(strings.TrimSpace(sk.Name))
	b.WriteString("\n\n## When To Use\n")
	b.WriteString(defaultString(sk.WhenToUse, sk.Name))
	b.WriteString("\n\n## Procedure\n")
	for i, step := range sk.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(step))
	}
	if len(sk.Pitfalls) > 0 {
		b.WriteString("\n## Known Pitfalls\n")
		for _, pit := range sk.Pitfalls {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(pit))
		}
	}
	b.WriteString("\n## Evidence\n")
	fmt.Fprintf(&b, "- source_session: %s\n", sessionID)
	return b.String()
}
