package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/trajectory"
)

// Stage names double as extraction_state watermark keys.
const (
	stageFacts   = "facts"
	stageFailure = "failure"
	stageSkill   = "skill"
	stageCurator = "curator"

	// skillTurnThreshold gates the skill lens: a workflow can only be induced
	// from a session with enough steps.
	skillTurnThreshold = 6

	// lensCallTimeout bounds a single LLM extraction call.
	lensCallTimeout = 90 * time.Second
)

// Pipeline runs the nightly trajectory-distillation loop. It is self-contained
// (owns its store + extractor + KB publisher) so main.go can launch it as a
// background goroutine without touching the server package.
type Pipeline struct {
	store     *store.Store
	extractor *Extractor
	kb        *KB
	log       *slog.Logger

	// capturer (re)builds full-fidelity trajectory_events from native pi files
	// across all users before extraction. Nil when trajectory archiving is
	// disabled, in which case the loop falls back to the lossy DB projection via
	// store.SnapshotSession inside processSession.
	capturer *trajectory.Capturer

	// runInterval is how often the loop wakes; runAt is the target hour (local)
	// for the daily run. A tick that isn't near runAt is a no-op unless a run is
	// due (never ran today).
	interval time.Duration
}

// PipelineOptions configures a Pipeline.
type PipelineOptions struct {
	Store     *store.Store
	Extractor *Extractor
	KB        *KB
	Log       *slog.Logger
	// Capturer archives + re-parses native pi trajectories before each sweep.
	// Nil disables native capture (fall back to the DB projection).
	Capturer *trajectory.Capturer
	// Interval between wake-ups. Defaults to 1h; the loop runs the full sweep
	// each wake but only reprocesses sessions whose fingerprint moved, so a
	// smaller interval just means fresher KB at negligible cost when idle.
	Interval time.Duration
}

// NewPipeline builds a Pipeline. Returns nil if extraction is not enabled (no
// LLM configured), so the caller can skip launching the loop.
func NewPipeline(opts PipelineOptions) *Pipeline {
	if opts.Extractor == nil || !opts.Extractor.Enabled() {
		return nil
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	return &Pipeline{
		store:     opts.Store,
		extractor: opts.Extractor,
		kb:        opts.KB,
		log:       opts.Log,
		capturer:  opts.Capturer,
		interval:  interval,
	}
}

// Run drives the loop until ctx is cancelled. It runs one sweep immediately,
// then on each interval tick (same run-once-then-ticker shape as the server's
// RunMaintenance).
func (p *Pipeline) Run(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.runSweep(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.runSweep(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runSweep runs a full sweep over the daily window (48h back, wide enough to
// catch sessions that ended recently or were never extracted).
func (p *Pipeline) runSweep(ctx context.Context) {
	p.sweep(ctx, time.Now().UTC().Add(-48*time.Hour))
}

// RunOnce runs a single bounded sweep scoped to `since` and returns any error
// from the enumeration step. Used by the replay CLI to reprocess a specific
// window without launching the ticker loop.
func (p *Pipeline) RunOnce(ctx context.Context, since time.Time) error {
	if p == nil {
		return nil
	}
	p.sweep(ctx, since)
	return nil
}

// sweep processes every session that has changed since `since`: (re)build
// native trajectory_events for all users, then per-session normalize → extract
// (3 lenses) → consolidate, then curate + publish once.
func (p *Pipeline) sweep(ctx context.Context, since time.Time) {
	// Native capture first: rebuild full-fidelity trajectory_events from the
	// on-host pi JSONL across ALL users, so the per-session lens extraction below
	// reads full-fidelity data rather than the lossy DB projection.
	if p.capturer != nil {
		results, err := p.capturer.CaptureAll(ctx, since)
		if err != nil {
			p.log.Warn("distillation: native capture failed", "error", err)
		} else {
			p.log.Info("distillation native capture complete", "captured_sessions", len(results))
		}
	}

	snaps, err := p.store.ListSessionsForDistillation(ctx, since)
	if err != nil {
		p.log.Warn("distillation: list sessions failed", "error", err)
		return
	}
	if len(snaps) == 0 {
		return
	}
	p.log.Info("distillation sweep starting", "candidate_sessions", len(snaps))

	processed := 0
	for _, snap := range snaps {
		if ctx.Err() != nil {
			return
		}
		if err := p.processSession(ctx, snap.SessionID); err != nil {
			p.log.Warn("distillation: session failed", "session_id", snap.SessionID, "error", err)
			continue
		}
		processed++
	}

	report, err := p.curateAndPublish(ctx)
	if err != nil {
		p.log.Warn("distillation: curate/publish failed", "error", err)
	}
	p.log.Info("distillation sweep complete", "sessions_processed", processed, "published_skills", report.PublishedSkills)
	if p.kb != nil {
		if err := p.kb.WriteReport(report); err != nil {
			p.log.Warn("distillation: write report failed", "error", err)
		}
	}
}

// processSession normalizes one session and runs the three lenses over it,
// each gated by a cheap signal and its own per-stage watermark.
func (p *Pipeline) processSession(ctx context.Context, sessionID string) error {
	// Only project the lossy DB snapshot when native capture is disabled. When a
	// capturer ran, trajectory_events already hold full-fidelity native data and
	// SnapshotSession would overwrite them with the lossy projection.
	if p.capturer == nil {
		if err := p.store.SnapshotSession(ctx, sessionID); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
	}
	events, err := p.store.GetTrajectoryEvents(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}
	if len(events) == 0 {
		return nil
	}
	turns := countUserTurns(events)

	p.runFactsLens(ctx, sessionID, events, turns)
	p.runFailureLens(ctx, sessionID, events, turns)
	p.runSkillLens(ctx, sessionID, events, turns)
	return nil
}

func (p *Pipeline) runFactsLens(ctx context.Context, sessionID string, events []store.TrajectoryEvent, turns int) {
	need, err := p.store.NeedsExtraction(ctx, sessionID, stageFacts, turns)
	if err != nil || !need {
		return
	}
	var out FactsResult
	callCtx, cancel := context.WithTimeout(ctx, lensCallTimeout)
	defer cancel()
	if err := p.extractor.Extract(callCtx, factsSystemPrompt, sliceDialogue(events), &out); err != nil {
		p.recordStage(ctx, sessionID, stageFacts, turns, "error", err)
		return
	}
	for _, m := range out.Memories {
		p.upsertMemory(ctx, sessionID, m)
	}
	p.recordStage(ctx, sessionID, stageFacts, turns, "ok", nil)
}

func (p *Pipeline) runFailureLens(ctx context.Context, sessionID string, events []store.TrajectoryEvent, turns int) {
	// Hard gate: only spend an LLM call on a material failure.
	if !hasFailureSignal(events) {
		p.recordStage(ctx, sessionID, stageFailure, turns, "skipped", nil)
		return
	}
	need, err := p.store.NeedsExtraction(ctx, sessionID, stageFailure, turns)
	if err != nil || !need {
		return
	}
	var out FailureResult
	callCtx, cancel := context.WithTimeout(ctx, lensCallTimeout)
	defer cancel()
	if err := p.extractor.Extract(callCtx, failureSystemPrompt, sliceAnnotated(events), &out); err != nil {
		p.recordStage(ctx, sessionID, stageFailure, turns, "error", err)
		return
	}
	// Persist the episode summary.
	lessons, _ := json.Marshal(out.Lessons)
	if _, err := p.store.InsertEpisodeMemory(ctx, store.EpisodeMemory{
		SessionID:       sessionID,
		Task:            out.Task,
		Outcome:         normalizeOutcome(out.Outcome),
		UserFrustration: normalizeFrustration(out.UserFrustration),
		FailedSteps:     json.RawMessage(lessons),
		Model:           p.extractor.cfg.Model,
	}); err != nil {
		p.log.Warn("distillation: insert episode failed", "session_id", sessionID, "error", err)
	}
	// Each lesson becomes a semantic-memory candidate (bug/heuristic/procedure).
	for _, m := range out.Lessons {
		p.upsertMemory(ctx, sessionID, m)
	}
	p.recordStage(ctx, sessionID, stageFailure, turns, "ok", nil)
}

func (p *Pipeline) runSkillLens(ctx context.Context, sessionID string, events []store.TrajectoryEvent, turns int) {
	if turns < skillTurnThreshold {
		p.recordStage(ctx, sessionID, stageSkill, turns, "skipped", nil)
		return
	}
	need, err := p.store.NeedsExtraction(ctx, sessionID, stageSkill, turns)
	if err != nil || !need {
		return
	}
	var out SkillResult
	callCtx, cancel := context.WithTimeout(ctx, lensCallTimeout)
	defer cancel()
	if err := p.extractor.Extract(callCtx, skillSystemPrompt, sliceReasoning(events), &out); err != nil {
		p.recordStage(ctx, sessionID, stageSkill, turns, "error", err)
		return
	}
	for _, sk := range out.Skills {
		p.upsertSkillCandidate(ctx, sessionID, sk)
	}
	p.recordStage(ctx, sessionID, stageSkill, turns, "ok", nil)
}

func (p *Pipeline) upsertMemory(ctx context.Context, sessionID string, m CandidateMemory) {
	statement := strings.TrimSpace(m.Statement)
	if statement == "" {
		return
	}
	if _, err := p.store.UpsertSemanticMemory(ctx, store.SemanticMemory{
		Type:        normalizeMemType(m.Type),
		Scope:       defaultString(m.Scope, "global"),
		Statement:   statement,
		WhenToApply: strings.TrimSpace(m.WhenToApply),
		Confidence:  clampConfidence(m.Confidence),
	}, sessionID); err != nil {
		p.log.Warn("distillation: upsert memory failed", "session_id", sessionID, "error", err)
	}
}

func (p *Pipeline) upsertSkillCandidate(ctx context.Context, sessionID string, sk CandidateSkill) {
	name := strings.TrimSpace(sk.Name)
	if name == "" || len(sk.Steps) == 0 {
		return
	}
	body := renderSkillMarkdown(sk, sessionID)
	if _, err := p.store.UpsertSkillCandidate(ctx, store.SkillCandidate{
		TargetSkillName:    slugify(name),
		Operation:          "create",
		Title:              name,
		BodyMD:             body,
		EvidenceSessionIDs: []string{sessionID},
		Confidence:         clampConfidence(sk.Confidence),
		Status:             "draft",
		KBStatus:           "observing",
	}); err != nil {
		p.log.Warn("distillation: upsert skill candidate failed", "session_id", sessionID, "error", err)
	}
}

func (p *Pipeline) recordStage(ctx context.Context, sessionID, stage string, turns int, status string, cause error) {
	st := store.ExtractionState{
		SessionID:          sessionID,
		Stage:              stage,
		ProcessedTurnCount: turns,
		Status:             status,
	}
	if cause != nil {
		st.Error = cause.Error()
	}
	if err := p.store.SetExtractionState(ctx, st); err != nil {
		p.log.Warn("distillation: set extraction state failed", "session_id", sessionID, "stage", stage, "error", err)
	}
}
