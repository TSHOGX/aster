package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/deploymentgate"
	"log/slog"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
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
	// store is the MEMORY database. Every write the pipeline performs lands
	// there; it holds no control-plane handle.
	store *memorystore.Store
	// ingest is the control -> memory boundary, used only for the lossy
	// projection fallback (SnapshotSession) when native capture is disabled.
	ingest    *memorystore.Ingestor
	extractor *Extractor
	kb        *KB
	log       *slog.Logger

	// capturer (re)builds full-fidelity trajectory_events from native pi files
	// across all users before extraction. Nil when trajectory archiving is
	// disabled, in which case the loop falls back to the lossy control-plane
	// projection via the ingest boundary inside processSession.
	capturer *trajectory.Capturer

	// agent drives the agentic context-evolution graph (consolidate/verify/
	// curate/critic). Nil ⇒ the scalar curateAndPublish path is used instead.
	agent *AgentRunner
	// agentic selects the agentic graph over the scalar path when true AND agent
	// is enabled.
	agentic bool

	// autoPublish re-points the KB `current` symlink as soon as a cycle finishes.
	// False (the default) means a cycle only stages a snapshot for expert
	// approval — see kb_approval.go.
	autoPublish bool

	// runInterval is how often the loop wakes; runAt is the target hour (local)
	// for the daily run. A tick that isn't near runAt is a no-op unless a run is
	// due (never ran today).
	interval time.Duration
}

// PipelineOptions configures a Pipeline.
type PipelineOptions struct {
	// Store is the memory database.
	Store *memorystore.Store
	// Ingest is the control -> memory boundary. Required only when Capturer is
	// nil, i.e. when the loop must fall back to projecting the control plane's
	// messages into trajectory_events itself.
	Ingest    *memorystore.Ingestor
	Extractor *Extractor
	KB        *KB
	Log       *slog.Logger
	// Capturer archives + re-parses native pi trajectories before each sweep.
	// Nil disables native capture (fall back to the DB projection).
	Capturer *trajectory.Capturer
	// Agent, when enabled, runs the agentic context-evolution graph. Nil/disabled
	// falls back to the scalar consolidation/promotion path.
	Agent *AgentRunner
	// Agentic selects the agentic graph when true and Agent is enabled.
	Agentic bool
	// AutoPublish skips the expert-approval gate and publishes straight to
	// `current`. Default false: cycles stage snapshots for review.
	AutoPublish bool
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
		store:       opts.Store,
		ingest:      opts.Ingest,
		extractor:   opts.Extractor,
		kb:          opts.KB,
		log:         opts.Log,
		capturer:    opts.Capturer,
		agent:       opts.Agent,
		agentic:     opts.Agentic && opts.Agent.Enabled(),
		autoPublish: opts.AutoPublish,
		interval:    interval,
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
	deploymentgate.Run(func() { p.sweep(ctx, time.Now().UTC().Add(-48*time.Hour)) })
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

	// Harvest sessions in parallel. Each processSession makes HTTP lens calls
	// (the Extractor, not a pi subprocess) and writes candidates through the
	// store, whose single-connection pool serializes the writes. parMap bounds
	// concurrency (HARNESS_MEMORY_AGENT_CONCURRENCY) so we don't open unbounded
	// upstream glm connections. Serial before this was the cold-start bottleneck.
	oks := parMap(ctx, snaps, func(ctx context.Context, snap memorystore.TrajectorySnapshot) bool {
		if err := p.processSession(ctx, snap.SessionID); err != nil {
			p.log.Warn("distillation: session failed", "session_id", snap.SessionID, "error", err)
			return false
		}
		return true
	})
	processed := 0
	for _, ok := range oks {
		if ok {
			processed++
		}
	}

	if p.agentic {
		// Agentic graph: reflect → consolidate → verify → curate → critic →
		// publish. Replaces the scalar curateAndPublish (harvest above is shared).
		if err := p.runAgenticGraph(ctx, processed); err != nil {
			p.log.Warn("distillation: agentic graph failed", "error", err)
		}
		return
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
	// Only project the lossy control-plane snapshot when native capture is
	// disabled. When a capturer ran, trajectory_events already hold full-fidelity
	// native data and SnapshotSession would overwrite them with the lossy
	// projection.
	if p.capturer == nil && p.ingest != nil {
		if err := p.ingest.SnapshotSession(ctx, sessionID); err != nil {
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

func (p *Pipeline) runFactsLens(ctx context.Context, sessionID string, events []memorystore.TrajectoryEvent, turns int) {
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

func (p *Pipeline) runFailureLens(ctx context.Context, sessionID string, events []memorystore.TrajectoryEvent, turns int) {
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
	if _, err := p.store.InsertEpisodeMemory(ctx, memorystore.EpisodeMemory{
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

func (p *Pipeline) runSkillLens(ctx context.Context, sessionID string, events []memorystore.TrajectoryEvent, turns int) {
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
	if _, err := p.store.UpsertSemanticMemory(ctx, memorystore.SemanticMemory{
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
	if _, err := p.store.UpsertSkillCandidate(ctx, memorystore.SkillCandidate{
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
	st := memorystore.ExtractionState{
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
