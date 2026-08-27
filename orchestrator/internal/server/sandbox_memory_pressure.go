package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// Escalation for teardown that can never succeed.
//
// The 2026-08-19 outage was invisible for 22 hours not because it was unlogged
// but because every attempt logged the same WARN: 1343 identical lines, no
// severity change, no aggregate. Nothing distinguished "one transient failure" —
// genuinely routine — from "this generation has been unreclaimable since
// yesterday", which needs a human.
//
// Counting consecutive failures per generation is what separates them.

// runtimeCleanupFailureAlertThreshold is how many consecutive failures for one
// generation escalate to ERROR.
//
// Sized against the maintenance cadence rather than picked round: the recovery
// stage runs on the heartbeat interval (30s in the publish deployment), so 10
// consecutive failures means roughly five minutes of a generation refusing to be
// reclaimed. Long enough that a sandbox in the middle of a slow-but-working
// teardown does not trip it, short enough that a human hears about a wedged one
// while the session is still worth saving.
const runtimeCleanupFailureAlertThreshold = 10

// recordRuntimeCleanupFailure counts a failed teardown and reports whether this
// generation has crossed the escalation threshold.
func (s *Server) recordRuntimeCleanupFailure(generationID string) (attempts int, escalate bool) {
	s.cleanupFailureMu.Lock()
	defer s.cleanupFailureMu.Unlock()
	if s.cleanupFailures == nil {
		s.cleanupFailures = make(map[string]int)
	}
	s.cleanupFailures[generationID]++
	attempts = s.cleanupFailures[generationID]
	// Escalate on the threshold and then periodically, rather than on every
	// subsequent attempt: re-logging at ERROR 200 times a day recreates the very
	// noise that hid the last outage.
	if attempts == runtimeCleanupFailureAlertThreshold {
		return attempts, true
	}
	if attempts > runtimeCleanupFailureAlertThreshold && attempts%runtimeCleanupFailureAlertThreshold == 0 {
		return attempts, true
	}
	return attempts, false
}

// clearRuntimeCleanupFailures forgets a generation's failure history once it is
// reclaimed, so a later unrelated failure starts from zero and the map does not
// grow without bound.
func (s *Server) clearRuntimeCleanupFailures(generationID string) {
	s.cleanupFailureMu.Lock()
	defer s.cleanupFailureMu.Unlock()
	delete(s.cleanupFailures, generationID)
}

// sandboxMemoryPressureWarning is the in-band nudge sent to an agent whose memcg
// is close to its ceiling.
//
// Warning beats raising the limit. Both prior incidents hit the ceiling that had
// just been raised for the previous one (873 MB against 1 GiB in 2026-08-16, then
// 3.9 GB against 4 GiB in 2026-08-19), because the shmem an analysis job wants
// scales with its input, not with the cap. A ceiling only moves the threshold; a
// warning lets the agent converge — write results out, process in batches — while
// it still has a process to do it with.
func sandboxMemoryPressureWarning(usedBytes, limitBytes int64) string {
	percent := 0
	if limitBytes > 0 {
		percent = int(usedBytes * 100 / limitBytes)
	}
	return fmt.Sprintf(
		"⚠️ 当前沙箱内存已用约 %s / %s（%d%%），接近上限。\n\n"+
			"请立即把已完成的结果落盘，改用分批/流式处理，并释放不再需要的大对象（尤其是 /dev/shm 中的临时文件）。"+
			"超过上限时进程会被直接终止，未落盘的数据会丢失。",
		formatGiB(usedBytes), formatGiB(limitBytes), percent)
}

func formatGiB(bytes int64) string {
	if bytes <= 0 {
		return "未知"
	}
	return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(1<<30))
}

// sandboxMemoryPressureThreshold is the fraction of the memcg limit that triggers
// the warning. 85% leaves the agent room to write results out before the kernel
// steps in, while staying high enough that a job with a normal working set never
// sees it.
const sandboxMemoryPressureThreshold = 0.85

// sandboxMemoryPressureCooldown bounds how often one generation is warned, so a
// job that sits at 90% for an hour gets a usable nudge rather than a warning on
// every poll.
const sandboxMemoryPressureCooldown = 2 * time.Minute

// warnPressuredSandboxes writes a memory-pressure notice into the transcript of
// any live sandbox close to its ceiling.
//
// The notice goes to the transcript rather than only to the logs because the
// agent reads the transcript: this is the one channel that can change the outcome
// while the job is still running. A log line warns an operator who is not
// watching; a transcript entry tells the process that is about to be killed.
func (s *Server) warnPressuredSandboxes(ctx context.Context, now time.Time) {
	generations, err := s.store.ListBridgePollGenerations(ctx, store.GenerationLeaseOwner(s.ownerUUID), now, s.cfg.Harness.Bridge.AckStartedGrace.Duration)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("memory pressure generation list failed", "error", err)
		}
		return
	}
	for _, generation := range generations {
		runtimeID := store.GenerationRunscContainerID(generation.GenerationID)
		used, limit, ok := s.runtime.SandboxMemoryUsage(ctx, runtimeID)
		if !ok || limit <= 0 {
			continue
		}
		if float64(used) < float64(limit)*sandboxMemoryPressureThreshold {
			s.clearMemoryPressureWarning(generation.GenerationID)
			continue
		}
		if !s.shouldWarnMemoryPressure(generation.GenerationID, now) {
			continue
		}
		s.log.Warn("sandbox approaching its memory limit",
			"session_id", generation.SessionID, "generation_id", generation.GenerationID,
			"used_bytes", used, "limit_bytes", limit)
		if _, err := s.store.AddAssistantMessage(ctx, generation.SessionID, "assistant", sandboxMemoryPressureWarning(used, limit), ""); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.log.Warn("failed to record memory pressure warning",
				"session_id", generation.SessionID, "generation_id", generation.GenerationID, "error", err)
		}
	}
}

// shouldWarnMemoryPressure rate-limits the warning per generation.
func (s *Server) shouldWarnMemoryPressure(generationID string, now time.Time) bool {
	s.memoryPressureMu.Lock()
	defer s.memoryPressureMu.Unlock()
	if s.memoryPressureWarned == nil {
		s.memoryPressureWarned = make(map[string]time.Time)
	}
	if last, seen := s.memoryPressureWarned[generationID]; seen && now.Sub(last) < sandboxMemoryPressureCooldown {
		return false
	}
	s.memoryPressureWarned[generationID] = now
	return true
}

// clearMemoryPressureWarning forgets a generation once it drops back below the
// threshold, so a later spike warns promptly and the map stays bounded.
func (s *Server) clearMemoryPressureWarning(generationID string) {
	s.memoryPressureMu.Lock()
	defer s.memoryPressureMu.Unlock()
	delete(s.memoryPressureWarned, generationID)
}
