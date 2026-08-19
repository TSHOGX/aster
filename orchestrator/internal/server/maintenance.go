package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/store"
)

func (s *Server) RunMaintenance(ctx context.Context) error {
	if strings.TrimSpace(s.ownerUUID) == "" {
		return fmt.Errorf("maintenance requires owner uuid")
	}
	heartbeatInterval := s.cfg.Harness.Bridge.HeartbeatInterval.Duration
	if heartbeatInterval <= 0 {
		heartbeatInterval = 30 * time.Second
	}
	pollInterval := s.cfg.Harness.Bridge.PollInterval.Duration
	if pollInterval <= 0 {
		pollInterval = 5 * time.Millisecond
	}
	coldFallbackInterval := s.cfg.Harness.Maintenance.ColdFallbackInterval.Duration
	if coldFallbackInterval <= 0 {
		coldFallbackInterval = 5 * time.Second
	}
	owner := store.GenerationLeaseOwner(s.ownerUUID)
	processor := &bridge.Processor{
		Store:           bridgeStore(s.store),
		Owner:           owner,
		LeaseTTL:        s.cfg.Harness.Bridge.LeaseTTL.Duration,
		AckStartedGrace: s.cfg.Harness.Bridge.AckStartedGrace.Duration,
		AfterCommit:     s.handleBridgeCommittedEnvelope,
	}
	touchHostHeartbeat := func(generation store.BridgePollGeneration, now time.Time) {
		if err := bridge.TouchHeartbeat(generation.BridgeDirPath, bridge.HostHeartbeatFile, now); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("bridge host heartbeat failed", "session_id", generation.SessionID, "generation_id", generation.GenerationID, "error", err)
		}
	}

	// stage isolates one maintenance step. Each step gets its own deadline and
	// runs on its own goroutine, so a step blocked in an unresponsive syscall
	// (historically `runsc kill` against a half-dead sandbox) can no longer stall
	// the whole loop and starve everything queued behind it. A step that outlives
	// its deadline is left running and skipped on subsequent ticks until it
	// returns, which also guarantees a step never overlaps itself.
	//
	// The goroutine is deliberately not abandoned-and-forgotten: `running` is only
	// cleared when the work actually finishes, so a permanently wedged stage stays
	// visible in the logs on every tick instead of silently piling up goroutines.
	stageTimeout := s.cfg.Harness.Maintenance.StageTimeout.Duration
	if stageTimeout <= 0 {
		stageTimeout = 2 * time.Minute
	}
	var stageMu sync.Mutex
	stageRunning := make(map[string]time.Time)
	stage := func(name string, fn func(context.Context)) {
		stageMu.Lock()
		if since, busy := stageRunning[name]; busy {
			stageMu.Unlock()
			s.log.Warn("maintenance stage still running, skipping this tick",
				"stage", name, "stuck_for", time.Since(since).Round(time.Second))
			return
		}
		stageRunning[name] = time.Now()
		stageMu.Unlock()

		stageCtx, cancel := context.WithTimeout(ctx, stageTimeout)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				stageMu.Lock()
				delete(stageRunning, name)
				stageMu.Unlock()
			}()
			defer cancel()
			fn(stageCtx)
		}()
		// Wait on an independent timer rather than stageCtx.Done(): the goroutine's
		// own `defer cancel()` fires before `defer close(done)`, so watching the
		// context here would report a deadline for a stage that had in fact just
		// finished successfully.
		timer := time.NewTimer(stageTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-timer.C:
			// Deadline hit. Return to the loop; the goroutine keeps draining in the
			// background and blocks only its own stage.
			s.log.Warn("maintenance stage exceeded deadline, continuing without it",
				"stage", name, "timeout", stageTimeout)
		}
	}

	// Cold fallback is the availability-critical stage: it starts a fresh sandbox
	// for sessions whose generation died with turns still queued. When it is
	// starved every user's next message hangs forever, so it gets both its own
	// ticker and its own worker below — no cleanup stage is worth that.
	runColdFallback := func() {
		stage("cold_fallback", func(stageCtx context.Context) {
			s.startColdFallbackSessions(stageCtx, owner)
		})
	}

	// Ticks are dispatched to workers over one-slot channels, and the select loop
	// below only ever does a non-blocking send. This is what actually keeps the
	// loop responsive: `stage` blocks until its work finishes or its deadline
	// expires, so calling it inline would stall the loop for the whole deadline
	// and starve the other tickers — exactly the failure being fixed, just with a
	// bound. A full channel means that worker is still busy, so the tick is
	// dropped rather than queued; there is no value in replaying a stale one.
	spawnWorker := func(run func(time.Time)) chan<- time.Time {
		ticks := make(chan time.Time, 1)
		go func() {
			for {
				select {
				case now := <-ticks:
					run(now)
				case <-ctx.Done():
					return
				}
			}
		}()
		return ticks
	}
	dispatch := func(ticks chan<- time.Time, now time.Time, label string) {
		select {
		case ticks <- now:
		default:
			s.log.Warn("maintenance worker still busy, dropping tick", "worker", label)
		}
	}

	runMaintenance := func(now time.Time) {
		stage("session_expiry", func(stageCtx context.Context) {
			if s.cfg.SessionRetention == 0 {
				if _, err := s.store.ClearActiveSessionExpiry(stageCtx, now); err != nil && !errors.Is(err, context.Canceled) {
					s.log.Warn("active-session expiry clear failed", "error", err)
				}
			}
			if _, err := s.store.SweepExpiredSessions(stageCtx, now); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("expired-session sweep failed", "error", err)
			}
			if _, err := s.store.SweepExpiredAuthSessions(stageCtx, now); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("expired auth-session sweep failed", "error", err)
			}
		})
		stage("share_links", func(stageCtx context.Context) {
			// Public-link access counters are buffered in memory by the anonymous read
			// handlers and land here, off the request path, so an anonymous fetch never
			// queues behind the writer pool's single connection.
			if err := s.flushShareLinkAccess(stageCtx); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("share link access flush failed", "error", err)
			}
			s.sweepExpiredShareLinks(stageCtx, now)
		})
		s.loginLimiter.GC()
		s.ipLimiter.GC()
		stage("pending_turn_cleanup", func(stageCtx context.Context) {
			if _, err := s.store.CancelTerminalSessionPendingTurns(stageCtx, now); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("terminal-session turn cleanup failed", "error", err)
			}
		})
		// Shells out to `runsc kill`/`delete` — the stage that wedged in the 34h
		// outage. Isolated so a half-dead sandbox can only block recovery itself.
		stage("runtime_recovery", func(stageCtx context.Context) {
			if _, err := s.RecoverExpiredRuntimeResources(stageCtx, now); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("allocation recovery failed", "error", err)
			}
		})
		stage("lease_renewal", func(stageCtx context.Context) {
			if _, err := s.store.RenewLiveGenerationLeases(stageCtx, store.RenewLiveGenerationsParams{
				Owner:      owner,
				LeaseTTL:   s.cfg.Harness.Bridge.LeaseTTL.Duration,
				Now:        now,
				StallGrace: s.cfg.Harness.Bridge.SandboxHeartbeatStallGrace.Duration,
			}); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("generation lease renewal failed", "error", err)
			}
		})
		// Cold fallback is deliberately absent here: it runs on its own goroutine
		// and ticker (see below) so the cleanup stages can never delay it.
		stage("host_heartbeat", func(stageCtx context.Context) {
			generations, err := s.store.ListBridgePollGenerations(stageCtx, owner, now, s.cfg.Harness.Bridge.AckStartedGrace.Duration)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.log.Warn("bridge heartbeat generation list failed", "error", err)
				}
				return
			}
			for _, generation := range generations {
				touchHostHeartbeat(generation, now)
			}
		})
		stage("checkpoint_retirement", func(stageCtx context.Context) {
			retiredCheckpoints, err := s.store.RetireExpiredCheckpoints(stageCtx, store.RetireExpiredCheckpointsParams{
				OwnerUUID:                s.ownerUUID,
				Now:                      now,
				CheckpointImageRetention: s.cfg.Harness.Reaper.CheckpointImageRetention.Duration,
			})
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.log.Warn("checkpoint retirement failed", "error", err)
				}
				return
			}
			for _, retired := range retiredCheckpoints {
				s.publishDurableEvent(stageCtx, retired.EventID)
			}
		})
		stage("resource_reaper", func(stageCtx context.Context) {
			if _, err := s.store.ReapResources(stageCtx, store.ReaperParams{
				OwnerUUID:       s.ownerUUID,
				FailedRetention: s.cfg.Harness.Reaper.FailedRetention.Duration,
				Now:             now,
			}); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("resource reaper failed", "error", err)
			}
		})
		// Also shells out to runsc teardown.
		stage("resource_destroy", func(stageCtx context.Context) {
			s.destroyReclaimableGenerationResources(stageCtx, now)
		})
		stage("event_prune", func(stageCtx context.Context) {
			if _, err := s.store.PruneEvents(stageCtx, store.PruneEventsParams{
				RetentionWindow: s.cfg.Harness.Events.RetentionWindow.Duration,
				RetentionRows:   s.cfg.Harness.Events.RetentionRows,
				Now:             now,
			}); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("event retention prune failed", "error", err)
			}
		})
	}
	pollBridge := func(now time.Time) {
		generations, err := s.store.ListBridgePollGenerations(ctx, owner, now, s.cfg.Harness.Bridge.AckStartedGrace.Duration)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.log.Warn("bridge generation list failed", "error", err)
			}
			return
		}
		for _, generation := range generations {
			processor.MarkReady(generation.SessionID, generation.GenerationID)
			if err := processor.ProcessOnce(ctx, generation.BridgeDirPath); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				s.log.Warn("bridge poll failed", "session_id", generation.SessionID, "generation_id", generation.GenerationID, "error", err)
			}
		}
	}

	// Cold fallback drives itself on its own goroutine and ticker, entirely
	// independent of the main loop. It must not be fed by the loop below: the
	// synchronous startup pass and any wedged cleanup stage would delay its
	// ticks, and starving this path is precisely what turned one dead sandbox
	// into a platform-wide outage.
	go func() {
		runColdFallback()
		ticker := time.NewTicker(coldFallbackInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runColdFallback()
			case <-ctx.Done():
				return
			}
		}
	}()

	// One full pass synchronously before entering the loop, so startup recovery
	// and the first host heartbeat are done before this returns — callers depend
	// on that ordering. Unlike before, every stage here is bounded by
	// stageTimeout, so a wedged teardown can no longer hang startup forever.
	runMaintenance(time.Now().UTC())
	pollBridge(time.Now().UTC())

	// Steady state: cleanup stages and bridge polling get their own workers, so
	// neither can delay the other. Bridge polling drives live turn I/O.
	maintenanceTicks := spawnWorker(runMaintenance)
	bridgeTicks := spawnWorker(pollBridge)

	maintenanceTicker := time.NewTicker(heartbeatInterval)
	defer maintenanceTicker.Stop()
	bridgeTicker := time.NewTicker(pollInterval)
	defer bridgeTicker.Stop()
	for {
		select {
		case now := <-maintenanceTicker.C:
			dispatch(maintenanceTicks, now.UTC(), "maintenance")
		case now := <-bridgeTicker.C:
			dispatch(bridgeTicks, now.UTC(), "bridge_poll")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) RecoverExpiredRuntimeResources(ctx context.Context, now time.Time) (store.StartupRecoveryResult, error) {
	if strings.TrimSpace(s.ownerUUID) == "" {
		return store.StartupRecoveryResult{}, fmt.Errorf("runtime recovery requires owner uuid")
	}
	params := store.StartupRecoveryParams{
		OwnerUUID:       s.ownerUUID,
		Now:             now,
		LeaseTTL:        s.cfg.Harness.Bridge.LeaseTTL.Duration,
		ReconnectGrace:  s.cfg.Harness.Bridge.ReconnectGrace.Duration,
		AckStartedGrace: s.cfg.Harness.Bridge.AckStartedGrace.Duration,
	}
	candidates, err := s.store.ListExpiredRuntimeRecoveryCandidates(ctx, params)
	if err != nil {
		return store.StartupRecoveryResult{}, err
	}
	cleaned := make([]store.ExpiredRuntimeRecoveryCandidate, 0, len(candidates))
	result := store.StartupRecoveryResult{}
	for _, candidate := range candidates {
		runtimeID := strings.TrimSpace(candidate.RuntimeID)
		if runtimeID == "" {
			result.RuntimeCleanupSkipped++
			s.log.Warn("recovery candidate has no runtime id", "session_id", candidate.SessionID, "generation_id", candidate.GenerationID)
			continue
		}
		if err := s.runtime.Destroy(ctx, runtimeID); err != nil {
			if errors.Is(err, context.Canceled) {
				return result, err
			}
			result.RuntimeCleanupSkipped++
			s.log.Warn("runtime cleanup before recovery failed", "session_id", candidate.SessionID, "generation_id", candidate.GenerationID, "runtime_id", runtimeID, "error", err)
			continue
		}
		cleaned = append(cleaned, candidate)
	}
	repaired, err := s.store.RepairExpiredRuntimeRecovery(ctx, params, cleaned)
	if err != nil {
		return result, err
	}
	repaired.RuntimeCleanupSkipped += result.RuntimeCleanupSkipped
	for _, eventID := range repaired.EventIDs {
		s.publishDurableEvent(ctx, eventID)
	}
	return repaired, nil
}

func (s *Server) DestroyReclaimableGenerationResources(ctx context.Context, now time.Time) {
	s.destroyReclaimableGenerationResources(ctx, now)
}

func (s *Server) destroyReclaimableGenerationResources(ctx context.Context, now time.Time) {
	candidates, err := s.store.ListDestroyableReclaimableGenerations(ctx, now, s.cfg.Harness.Reaper.FailedRetention.Duration)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("destroyable resource list failed", "error", err)
		}
		return
	}
	for _, candidate := range candidates {
		if err := s.cleanupGenerationResources(ctx, candidate.SessionID, candidate.GenerationID, now); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("generation resource cleanup failed", "session_id", candidate.SessionID, "generation_id", candidate.GenerationID, "error", err)
		}
	}
}

func (s *Server) cleanupGenerationResources(ctx context.Context, sessionID, generationID string, now time.Time) error {
	details, err := s.store.GetRuntimeGenerationDetails(ctx, sessionID, generationID)
	if err != nil {
		return fmt.Errorf("lookup generation resources: %w", err)
	}
	resourceInstance, resourceTracked, err := s.runtimeResourceCleanupIdentityIfExists(ctx, generationID)
	if err != nil {
		return fmt.Errorf("lookup runtime resource instance: %w", err)
	}
	if resourceTracked {
		if err := s.claimRuntimeResourceCleanup(ctx, resourceInstance, now); err != nil {
			return err
		}
		details = runtimeDetailsWithResourceInstance(details, resourceInstance)
	}
	cleanup, err := s.runtime.DestroyGenerationResources(ctx, details)
	if err != nil {
		return fmt.Errorf("destroy generation resources: %w", err)
	}
	if err := s.store.MarkGenerationResourcesDestroyed(ctx, store.DestroyGenerationResourcesParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		Now:          now,
	}); err != nil {
		return fmt.Errorf("mark generation resources destroyed: %w", err)
	}
	if resourceTracked {
		if err := s.completeRuntimeResourceCleanup(ctx, resourceInstance, cleanup, now); err != nil {
			return fmt.Errorf("mark runtime resource destroyed: %w", err)
		}
	}
	return nil
}

func (s *Server) handleBridgeCommittedEnvelope(ctx context.Context, envelope bridge.Envelope, eventID int64) {
	s.publishDurableEvent(ctx, eventID)
	switch envelope.Type {
	case bridge.TypeEmitOutput:
		s.handleBridgeOutput(ctx, envelope)
	case bridge.TypeAckTurnCompleted:
		s.handleBridgeCompletion(ctx, envelope)
		// See turn_resume.go: continue a turn whose model call died on a
		// transient upstream fault, rather than leaving the reply truncated.
		s.maybeResumeFailedTurn(ctx, envelope)
	}
}

func (s *Server) publishDurableEvent(ctx context.Context, eventID int64) {
	if eventID == 0 {
		return
	}
	record, ok, err := s.store.GetEvent(ctx, eventID)
	if err != nil {
		s.log.Warn("failed to load durable event", "event_id", eventID, "error", err)
		return
	}
	if !ok {
		return
	}
	s.hub.Publish(eventFromRecord(record))
}

func (s *Server) handleBridgeOutput(ctx context.Context, envelope bridge.Envelope) {
	var payload struct {
		Stream  string          `json:"stream"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		s.log.Warn("failed to decode bridge output payload", "session_id", envelope.SessionID, "generation_id", envelope.GenerationID, "error", err)
		return
	}
	stream := payload.Stream
	if stream == "" {
		stream = "stdout"
	}
	if len(payload.Payload) == 0 {
		return
	}
	parser := s.bridgeStreamParser(ctx, envelope)
	parser.handleBridgeOutput(normalizerBridgeOutput{Stream: stream, Payload: payload.Payload})
}

func (s *Server) handleBridgeCompletion(ctx context.Context, envelope bridge.Envelope) {
	s.completeBridgeStreamParser(envelope)
	if err := s.watcher.ScanSession(ctx, envelope.SessionID); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("failed to scan bridge-completed session artifacts", "session_id", envelope.SessionID, "error", err)
	}
}

// bridgeStreamParser returns the per-turn parser, creating it on first output.
// The session's agent is constant for the turn, so it is read once when the
// parser is created and cached on the parser (via its driverID); the steady
// token hot path reuses the cached parser and issues zero GetSession calls.
func (s *Server) bridgeStreamParser(ctx context.Context, envelope bridge.Envelope) *streamParser {
	key, ok := bridgeParserKey(envelope)
	if !ok {
		return newStreamParser(s, envelope.SessionID, s.sessionAgent(ctx, envelope.SessionID))
	}
	s.bridgeParserMu.Lock()
	defer s.bridgeParserMu.Unlock()
	if s.bridgeParsers == nil {
		s.bridgeParsers = make(map[bridgeStreamParserKey]*streamParser)
	}
	parser := s.bridgeParsers[key]
	if parser == nil {
		parser = newStreamParser(s, envelope.SessionID, s.sessionAgent(ctx, envelope.SessionID))
		parser.turnID = key.TurnID
		s.bridgeParsers[key] = parser
	}
	return parser
}

// sessionAgent loads the session's configured agent, returning "" (and logging)
// on failure so an unsupported/empty driver surfaces through the normalizer
// rather than aborting output handling.
func (s *Server) sessionAgent(ctx context.Context, sessionID string) string {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		s.log.Warn("failed to load session for bridge output", "session_id", sessionID, "error", err)
		return ""
	}
	return session.Agent
}

func (s *Server) completeBridgeStreamParser(envelope bridge.Envelope) {
	key, ok := bridgeParserKey(envelope)
	if !ok {
		return
	}
	s.bridgeParserMu.Lock()
	parser := s.bridgeParsers[key]
	delete(s.bridgeParsers, key)
	s.bridgeParserMu.Unlock()
	if parser == nil {
		return
	}
	// Teardown flush on bridge disconnect: no turn-end event was seen, so the
	// terminator is unknown.
	parser.flush("")
	parser.complete()
}

func bridgeParserKey(envelope bridge.Envelope) (bridgeStreamParserKey, bool) {
	if envelope.TurnID == nil {
		return bridgeStreamParserKey{}, false
	}
	return bridgeStreamParserKey{
		SessionID:    envelope.SessionID,
		GenerationID: envelope.GenerationID,
		TurnID:       *envelope.TurnID,
	}, true
}

// sweepExpiredShareLinks deletes expired public links and reclaims the blobs
// that lost their last reference.
//
// Phase 2 links carry no files, so the returned digest list is empty; the unlink
// step arrives with the blob store. The sweep runs now anyway so expired rows do
// not accumulate, and so the reclamation path has one owner from the start rather
// than being bolted on next to a table that already has stale rows in it.
func (s *Server) sweepExpiredShareLinks(ctx context.Context, now time.Time) {
	orphaned, err := s.store.SweepExpiredShareLinks(ctx, now)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("expired share-link sweep failed", "error", err)
		}
		return
	}
	if len(orphaned) == 0 {
		return
	}
	if s.shareBlobs == nil {
		// Rows referenced blobs but no store is configured (file sharing was turned
		// off after links were made). Say so rather than dropping it silently: the
		// bytes are on disk and nothing else will report them.
		s.log.Warn("share link sweep reported unreferenced blobs with no blob store configured",
			"blob_count", len(orphaned))
		return
	}
	s.reclaimShareBlobs(orphaned)
}
