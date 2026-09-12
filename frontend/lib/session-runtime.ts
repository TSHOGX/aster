import { withActivity, type ConversationState } from "./harness-events";
import type { ApiSessionRuntime } from "./types";

/**
 * Applying the session runtime projection — the client's single source of truth
 * for whether the agent is busy, what it is doing, and what the user has queued.
 *
 * Kept pure and separate from the provider because the two rules below are the
 * load-bearing part of the design, and they are worth testing directly rather
 * than through a React tree.
 */

export type RuntimeSlice<TConversation> = {
  runtimes: Record<string, ApiSessionRuntime | null>;
  conversations: Record<string, TConversation>;
};

/**
 * Should this snapshot be applied over what is already held?
 *
 * Only when it is strictly newer. Requests and pushes race constantly: a GET
 * issued before a change can resolve after the push that reported it, and
 * applying that stale body would restore a queue entry the server has already
 * drained — which then never clears, because nothing contradicts it again. The
 * monotonic revision is what makes arrival order irrelevant.
 */
export function shouldApplyRuntime(
  held: ApiSessionRuntime | null | undefined,
  incoming: ApiSessionRuntime
): boolean {
  if (typeof incoming.revision !== "number") return false;
  if (!held) return true;
  return incoming.revision > held.revision;
}

/**
 * Reconcile a conversation's activity with the projection.
 *
 * Activity is strictly subordinate to the snapshot: no active turn means idle,
 * full stop. Live events refine the phase of a turn the snapshot says is running;
 * they never invent one. That single rule is what keeps an idle session from
 * displaying "Thinking" — it holds by construction, instead of needing every
 * event path that could leave a stale phase to remember to clear it.
 */
export function reduceRuntimeActivity<TConversation extends ConversationState>(
  conversation: TConversation,
  snapshot: ApiSessionRuntime
): TConversation {
  const active = snapshot.active_turn;
  if (!active) {
    // Streaming text belongs to a turn that is no longer running, so it is
    // dropped with the phase; a committed assistant message already holds it.
    return withActivity({ ...conversation, streaming: [] }, { phase: "idle" }) as TConversation;
  }
  return withActivity(conversation, {
    phase: active.phase ?? "thinking",
    toolName: active.tool_name ?? null,
    toolDetail: active.tool_detail ?? null
  }) as TConversation;
}
