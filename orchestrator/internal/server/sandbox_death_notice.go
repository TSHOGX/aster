package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"harness-platform/orchestrator/internal/store"
)

// Telling the user why their sandbox died.
//
// Before this, a sandbox death produced `error_class = unknown_after_ack_started`
// on the turn row and nothing else: no transcript entry, and
// repairRecoveredSessionTx explicitly cleared error_class/failure_reason off the
// session. The 2026-08-19 incident is the concrete cost — the user's last visible
// message was the agent saying it was starting a long job, then 22 hours of
// silence with no explanation anywhere in the UI.
//
// "unknown" was also inaccurate. cgroup v2 records an oom_kill counter per
// sandbox, so when the workload is OOM-killed the system does know; it just threw
// the fact away. Two things change here: the cause is diagnosed from the cgroup,
// and it is written into the transcript as a message the user actually sees.

// sandboxOOMCause marks a generation whose memcg recorded an OOM kill.
const sandboxOOMCause = "sandbox_oom"

// classifyRecoveryCauses annotates candidates with a diagnosed cause where one
// can be established.
//
// Read the cgroup before the repair runs, because reclaiming the generation's
// resources removes the cgroup directory and the evidence with it.
func (s *Server) classifyRecoveryCauses(ctx context.Context, candidates []store.ExpiredRuntimeRecoveryCandidate) []store.ExpiredRuntimeRecoveryCandidate {
	for i, candidate := range candidates {
		runtimeID := strings.TrimSpace(candidate.RuntimeID)
		if runtimeID == "" {
			continue
		}
		oomKilled, ok := s.runtime.SandboxOOMKilled(ctx, runtimeID)
		if !ok || !oomKilled {
			// Unknown stays unknown: claiming a cause we cannot support would be
			// worse than the generic notice.
			continue
		}
		candidates[i].Cause = sandboxOOMCause
		s.log.Info("diagnosed sandbox death as memory-limit kill",
			"session_id", candidate.SessionID, "generation_id", candidate.GenerationID)
	}
	return candidates
}

// sandboxDeathStopReason marks a notice written because the sandbox itself died,
// as opposed to piStopReasonError ("error"), which means the model call failed
// while the sandbox stayed healthy.
//
// The distinction matters to the UI, not just to logs: after a model-call failure
// the sandbox still holds the agent's context, so "continue where you left off"
// is accurate. After a sandbox death it is not — the context is gone — and
// offering the same wording invites the user to trust state that no longer
// exists. wasInterrupted treats both as interrupted; only the offered recovery
// differs.
const sandboxDeathStopReason = "sandbox_died"

// sandboxDeathNotice is the transcript message explaining a sandbox death.
//
// Written as an assistant message so it appears inline where the reply should
// have been, with a stop_reason that marks it as a notice rather than a real
// reply.
//
// The text states what survived and what did not, because that determines what
// the user should do next: the workspace and agent home are on host-side storage
// and persist into the replacement sandbox, while anything in /dev/shm or /tmp
// and the agent's in-memory context are gone. Users who are not told this
// reasonably assume "continue" picks up where the agent left off.
func sandboxDeathNotice(cause string, limitBytes int64) string {
	if cause == sandboxOOMCause {
		limit := "内存上限"
		if limitBytes > 0 {
			limit = fmt.Sprintf("内存上限（%.1f GiB）", float64(limitBytes)/float64(1<<30))
		}
		return "⚠️ 上一次任务因超出沙箱" + limit + "被系统终止。\n\n" +
			"常见原因是把大量数据一次性读进内存或写进 /dev/shm（例如整批 CSV/Parquet 一次载入）。\n\n" +
			"**已保留**：工作区文件、已落盘的产物。\n" +
			"**已丢失**：/dev/shm 与 /tmp 中的临时数据，以及我在上一个沙箱里的执行上下文。\n\n" +
			"继续之前建议改成分批处理、及时落盘，否则很可能再次触发同样的限制。"
	}
	return "⚠️ 上一次任务因沙箱异常终止而中断。\n\n" +
		"**已保留**：工作区文件、已落盘的产物。\n" +
		"**已丢失**：临时数据和我在上一个沙箱里的执行上下文。\n\n" +
		"可以让我从中断处继续，但我需要先重新确认当前进度。"
}

// recordSandboxDeathNotices appends the explanation only to repaired sessions
// that actually had leased/running work. Idle generation retirement is an
// infrastructure event, not an interrupted user task, and must stay invisible
// in the conversation transcript.
//
// Best-effort and deliberately non-fatal: recovery has already committed by the
// time this runs, and failing to write an explanation must not undo a repair that
// restored service. Each failure is logged instead.
func (s *Server) recordSandboxDeathNotices(ctx context.Context, candidates []store.ExpiredRuntimeRecoveryCandidate) {
	limitBytes := s.cfg.Harness.Runtime.SandboxMemoryLimitBytes
	for _, candidate := range candidates {
		if !candidate.InterruptedWork {
			// An idle generation can be retired after a restart/reconnect grace
			// timeout even though its last turn completed successfully. Do not tell
			// the user that a task was interrupted when there was no in-flight work.
			continue
		}
		notice := sandboxDeathNotice(candidate.Cause, limitBytes)
		if _, err := s.store.AddAssistantMessage(ctx, candidate.SessionID, "assistant", notice, sandboxDeathStopReason); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.log.Warn("failed to record sandbox death notice",
				"session_id", candidate.SessionID, "generation_id", candidate.GenerationID, "error", err)
		}
	}
}
