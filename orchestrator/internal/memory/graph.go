package memory

// The agentic context-evolution graph orchestrator (Phase 6 integration). It
// sequences the node implementations built in node_*.go into one cycle:
//
//	0 Reflect → [A Harvest already ran] → B Consolidate → C Verify →
//	D Curate → F Consistency-critic → PII backstop → E Publish → report
//
// Harvest (the HTTP lenses in pipeline.processSession) has already populated
// candidate memories/skills before this runs. This orchestrator turns those raw
// candidates into a published KB snapshot through the agent nodes, preserving
// the deterministic spine (content-addressed publish, PII backstop, atomic
// repoint) that must hold regardless of any agent's output.

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
)

// runAgenticGraph drives one full cycle. sessionsProcessed is the harvest count,
// recorded in the run summary. It never returns fatal errors for a single node's
// failure — each node degrades to hold/no-op and the cycle continues — but a
// setup failure (can't start the run row) is returned.
func (p *Pipeline) runAgenticGraph(ctx context.Context, sessionsProcessed int) error {
	// 0. Reflect — open a run row, plan the cycle.
	runID, err := p.store.StartEvolutionRun(ctx, "{}")
	if err != nil {
		return err
	}
	reflect := NewReflectNode(p.agent, p.store, p.log, runID)
	plan, _ := reflect.Run(ctx) // never fatal; ProcessAll on failure

	counts := map[string]int{"sessions": sessionsProcessed}
	if len(plan.ActiveScopes) == 0 && len(plan.SkipScopes) == 0 && !plan.ProcessAll {
		// Empty delta — nothing new to distill this cycle.
		p.log.Info("agentic cycle: empty delta, skipping", "run_id", runID)
		_ = p.store.FinishEvolutionRun(ctx, runID, mustJSONCounts(counts), "", "empty delta")
		return nil
	}

	// B. Consolidate — semantic clustering → converging support.
	if r, err := NewConsolidateNode(p.agent, p.store, p.log, runID).Run(ctx); err != nil {
		p.log.Warn("agentic: consolidate failed", "error", err)
	} else {
		counts["clusters_in"] = r.ClustersIn
		counts["clusters_out"] = r.ClustersOut
		// absorbed = raw candidates folded into multi-member clusters (the real
		// convergence signal); ClustersOut == ClustersIn means all singletons.
		counts["absorbed"] = r.ClustersIn - r.ClustersOut
		counts["kb_duplicates"] = r.KBDuplicates
		p.log.Info("agentic: consolidate done",
			"clusters_in", r.ClustersIn, "clusters_out", r.ClustersOut,
			"absorbed", r.ClustersIn-r.ClustersOut, "kb_dupes", r.KBDuplicates)
	}

	// C. Verify — check consolidated knowledge against the live tree.
	if r, err := NewVerifyNode(p.agent, p.store, p.log, runID).Run(ctx); err != nil {
		p.log.Warn("agentic: verify failed", "error", err)
	} else {
		counts["verified_stale"] = r.Stale
		counts["verified_fixed"] = r.Fixed
		p.log.Info("agentic: verify done", "stale", r.Stale, "fixed", r.Fixed, "current", r.Current)
	}

	// D. Curate — the promotion gate (memory one-pass, skill reflect-loop).
	// The promoted ids feed the critic below: reviewing only THIS cycle's additions
	// is what keeps that node's single call bounded as the KB grows.
	newMemoryIDs := map[int64]bool{}
	newSkillIDs := map[int64]bool{}
	if r, err := NewCurateNode(p.agent, p.store, p.log, runID).Run(ctx); err != nil {
		p.log.Warn("agentic: curate failed", "error", err)
	} else {
		counts["mem_promoted"] = r.MemPromoted
		counts["skill_promoted"] = r.SkillPromoted
		for _, id := range r.PromotedMemoryIDs {
			newMemoryIDs[id] = true
		}
		for _, id := range r.PromotedSkillIDs {
			newSkillIDs[id] = true
		}
		p.log.Info("agentic: curate done", "mem_promoted", r.MemPromoted, "skill_promoted", r.SkillPromoted)
	}

	// F. Consistency critic — the final global gate. Memory vetoes are applied
	// in-node (accepted→candidate); skill vetoes come back as ids to exclude.
	// Scoped to this cycle's promotions: the critic used to re-read the ENTIRE
	// accepted set (715 memories by run 4), which grows without bound and was
	// killed at the 12-minute ceiling every time — so the gate never once ran.
	// Everything already accepted was reviewed by the cycle that promoted it.
	vetoedSkills := map[int64]bool{}
	switch {
	case len(newMemoryIDs) == 0 && len(newSkillIDs) == 0:
		// Nothing was promoted (or curate failed), so there is nothing new to
		// review. Skipping matters: an empty review set means "review everything"
		// to filterMemoryReviewSet, which would put the whole accepted set back in
		// one call — exactly the unbounded shape this scoping removes.
		p.log.Info("agentic: critic skipped (no new promotions this cycle)")
	default:
		critic := NewCriticNode(p.agent, p.store, p.log, runID)
		critic.NewMemoryIDs = newMemoryIDs
		critic.NewSkillIDs = newSkillIDs
		if r, err := critic.Run(ctx); err != nil {
			p.log.Warn("agentic: critic failed", "error", err)
		} else {
			for _, id := range r.VetoedSkillIDs {
				vetoedSkills[id] = true
			}
			counts["vetoed_memories"] = r.VetoedMemories
			counts["vetoed_skills"] = r.VetoedSkills
			counts["critic_reviewed"] = r.ReviewedMemories + r.ReviewedSkills
			if r.Held {
				// The critic fails safe (no veto), which is right — but for four runs
				// that made "vetoed_memories=0" indistinguishable from a gate that
				// never ran. Record the distinction where a reviewer will see it.
				counts["critic_held"] = 1
				p.log.Warn("agentic: critic held (agent error) — no veto was applied; gate did NOT run")
			}
			p.log.Info("agentic: critic done",
				"reviewed", r.ReviewedMemories+r.ReviewedSkills,
				"vetoed_mem", r.VetoedMemories, "vetoed_skill", r.VetoedSkills)
		}
	}

	// E. Publish — assemble the final set (post-critic), run the PII backstop,
	// write a content-addressed snapshot. Whether that snapshot goes live or waits
	// for expert approval is decided by autoPublish inside publishAgentic.
	digest, published, staged, err := p.publishAgentic(ctx, runID, counts, vetoedSkills)
	if err != nil {
		p.log.Warn("agentic: publish failed", "error", err)
	}
	counts["published_skills"] = published
	if staged {
		counts["staged"] = 1
		p.log.Info("agentic cycle complete: snapshot STAGED, awaiting expert approval",
			"run_id", runID, "digest", digest, "skills", published,
			"approve_with", "orchestrator kb approve "+digest)
	} else {
		p.log.Info("agentic cycle complete", "run_id", runID, "digest", digest, "published_skills", published)
	}

	notes := "digest=" + digest
	if staged {
		notes = "staged=" + digest
	}
	_ = p.store.FinishEvolutionRun(ctx, runID, mustJSONCounts(counts), digest, notes)
	if p.kb != nil {
		_ = p.kb.WriteReport(p.agenticReport(counts, digest, staged))
	}
	return nil
}

// publishAgentic gathers the accepted memories + published skills (minus critic
// vetoes), applies the secret/PII backstop, and writes the KB snapshot. With
// autoPublish it re-points `current` immediately; otherwise the snapshot is only
// staged for expert approval (staged=true) and no sandbox can see it yet.
// Returns the snapshot digest, the count of skills in it, and whether it is
// awaiting approval.
func (p *Pipeline) publishAgentic(ctx context.Context, runID int64, counts map[string]int, vetoedSkills map[int64]bool) (string, int, bool, error) {
	if p.kb == nil {
		return "", 0, false, nil
	}
	accepted, err := p.store.ListSemanticMemories(ctx, "accepted")
	if err != nil {
		return "", 0, false, err
	}
	skills, err := p.store.ListSkillCandidates(ctx, "published")
	if err != nil {
		return "", 0, false, err
	}

	// Secret/PII backstop: the curator prompt already rejects these, but a
	// deterministic filter is the enforcement point — nothing carrying a secret
	// or private connection string reaches the shared KB even if an agent slips.
	safeMems := accepted[:0]
	for _, m := range accepted {
		if distinctEvidenceCount(m.EvidenceSessionIDs) < minFactSupport {
			continue
		}
		if containsSecret(m.Statement) || containsSecret(m.WhenToApply) {
			p.log.Warn("agentic: PII backstop dropped memory", "id", m.ID)
			continue
		}
		safeMems = append(safeMems, m)
	}
	var safeSkills []memorystore.SkillCandidate
	for _, sk := range skills {
		if distinctEvidenceCount(sk.EvidenceSessionIDs) < minSkillSupport {
			continue
		}
		if vetoedSkills[sk.ID] {
			continue // critic veto
		}
		if containsSecret(sk.BodyMD) {
			// A seeded skill (e.g. doris-query) legitimately embeds a credential;
			// those are carried forward by kb.carryForward, NOT re-published here.
			// A DISTILLED skill carrying a secret is dropped.
			p.log.Warn("agentic: PII backstop dropped distilled skill", "id", sk.ID)
			continue
		}
		safeSkills = append(safeSkills, sk)
	}

	if p.autoPublish {
		digest, err := p.kb.Publish(safeMems, safeSkills)
		if err != nil {
			return "", 0, false, err
		}
		return digest, len(safeSkills), false, nil
	}
	// Review-gated (default): stage only. changed=false means the cycle produced
	// content identical to what is already live, so there is nothing to approve.
	digest, changed, err := p.kb.Stage(safeMems, safeSkills, runID, counts)
	if err != nil {
		return "", 0, false, err
	}
	return digest, len(safeSkills), changed, nil
}

// secretPatterns backstops the curator: obvious credential/PII shapes that must
// never enter the shared KB from a DISTILLED memory/skill. Deliberately narrow
// (high precision) — the curator LLM is the recall mechanism; this is the
// deterministic floor.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`),             // OpenAI-style keys
	regexp.MustCompile(`(?i)password\s*[:=]\s*\S+`),          // password: xxx
	regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*\S+`),       // api_key: xxx
	regexp.MustCompile(`MYSQL_PWD\s*=\s*\S+`),                // inline db pwd
	regexp.MustCompile(`mysql://[^\s]*:[^\s]*@`),             // creds in a URL
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_\-\.]{20,}`), // bearer tokens
}

// containsSecret reports whether s matches any secret/PII backstop pattern.
func containsSecret(s string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// agenticReport builds the daily report for an agentic cycle.
func (p *Pipeline) agenticReport(counts map[string]int, digest string, staged bool) Report {
	notes := []string{
		"agentic graph: reflect→consolidate→verify→curate→critic→publish",
	}
	if counts["verified_stale"] > 0 || counts["verified_fixed"] > 0 {
		notes = append(notes, "verify pruned stale/fixed knowledge against live code")
	}
	if counts["critic_held"] > 0 {
		notes = append(notes, "⚠ consistency critic FAILED — no veto pass ran; vetoed counts below are absence of a check, not a clean result")
	}
	if staged {
		notes = append(notes, "STAGED awaiting expert approval — not live. Review: harness-ctl kb diff / approve "+digest)
	} else if digest != "" {
		notes = append(notes, "published live (auto_publish)")
	}
	return Report{
		GeneratedAt:      time.Now().UTC(),
		SessionsSwept:    counts["sessions"],
		AcceptedMemories: counts["mem_promoted"],
		PublishedSkills:  counts["published_skills"],
		Digest:           digest,
		Notes:            notes,
	}
}

// mustJSONCounts renders a counts map to a JSON string for the run row.
func mustJSONCounts(counts map[string]int) string {
	b, err := json.Marshal(counts)
	if err != nil {
		return "{}"
	}
	return string(b)
}
