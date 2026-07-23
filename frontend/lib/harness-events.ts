import { isRecord } from "./guards";
import {
  TRACE_RESULT_MAX,
  diffFromToolArgs,
  readToolArgs,
  readToolName,
  stringifyResult,
  toolDetailFromInput,
  truncate
} from "./trace";
import type { ApiMessage, TraceNode } from "./types";

export type ActivityPhase =
  | "idle"
  | "thinking"
  | "tool"
  | "streaming"
  | "compacting"
  | "retrying";

export type ConversationActivity = {
  phase: ActivityPhase;
  startedAt: number | null;
  requestCount: number;
  inflight: number;
  toolName: string | null;
  toolDetail: string | null;
};

// A live trace node carries the tool call id (when pi provides one) so the
// matching tool_execution_end can finalize the right node even when tools run
// concurrently. The id is internal and dropped when rendered as a TraceNode.
export type LiveTraceNode = TraceNode & { toolCallId?: string };

export type ConversationState = {
  messages: ApiMessage[];
  streaming: Array<{ id: string; text: string; deltaType?: string }>;
  liveTrace: LiveTraceNode[];
  activity: ConversationActivity;
  loading: boolean;
};

export function idleActivity(): ConversationActivity {
  return { phase: "idle", startedAt: null, requestCount: 0, inflight: 0, toolName: null, toolDetail: null };
}

export function emptyConvo(): ConversationState {
  return {
    messages: [],
    streaming: [],
    liveTrace: [] as LiveTraceNode[],
    activity: idleActivity(),
    loading: false
  };
}

// Monotonic clock for turn timers. Date.now would be fine, but performance.now
// is immune to wall-clock adjustments during a long-running turn.
export function nowMs(): number {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

export function appendStreaming(
  streaming: ConversationState["streaming"],
  id: string,
  text: string,
  deltaType?: string
): ConversationState["streaming"] {
  const idx = streaming.findIndex((entry) => entry.id === id);
  if (idx === -1) {
    return [...streaming, { id, text, deltaType }];
  }
  return streaming.map((entry, entryIdx) =>
    entryIdx === idx ? { ...entry, text: entry.text + text } : entry
  );
}

function isCommittedStream(streamText: string, messageText: string) {
  const stream = streamText.trim();
  const message = messageText.trim();
  return stream !== "" && message !== "" && (stream === message || message.startsWith(stream));
}

export function pruneCommittedStreaming(
  streaming: ConversationState["streaming"],
  messages: ApiMessage[]
): ConversationState["streaming"] {
  let next = streaming;
  for (const message of messages) {
    if (message.role !== "assistant") continue;
    const idx = next.findIndex((entry) => isCommittedStream(entry.text, message.content));
    if (idx !== -1) {
      next = [...next.slice(0, idx), ...next.slice(idx + 1)];
    }
  }
  return next;
}

export function withActivity(
  c: ConversationState,
  patch: Partial<ConversationActivity>
): ConversationState {
  const next: ConversationActivity = { ...c.activity, ...patch };
  // Any non-idle phase implies the turn is underway: start the timer once.
  if (next.phase !== "idle" && next.startedAt === null) {
    next.startedAt = nowMs();
  }
  if (next.phase === "idle") {
    next.startedAt = null;
    next.toolName = null;
    next.toolDetail = null;
  }
  return { ...c, activity: next };
}

// --- Pure conversation reducers ---------------------------------------------
// Each takes the current ConversationState (plus any already-parsed inputs) and
// returns the next state. They mirror the activity FSM the provider drove
// inline; keeping them here makes the transitions unit-testable and free of
// store/React concerns. Callers still wrap them in upsertConvo so store notify
// semantics are unchanged.

// A text_delta arrived: append it to the matching streaming buffer and mark the
// turn as streaming.
export function reduceAgentDelta(
  c: ConversationState,
  id: string,
  text: string,
  deltaType: string
): ConversationState {
  return withActivity(
    { ...c, streaming: appendStreaming(c.streaming, id, text, deltaType) },
    { phase: "streaming" }
  );
}

// A pi tool_execution_start / tool_execution_update frame: refine the activity
// strip to "tool" and, on start only, push an open live-trace node.
export function reduceToolActivity(
  c: ConversationState,
  payload: Record<string, unknown>,
  isStart: boolean
): ConversationState {
  const toolName = readToolName(payload);
  const args = readToolArgs(payload);
  const detail = toolDetailFromInput(args ?? payload.input);
  const toolCallId = typeof payload.toolCallId === "string" ? payload.toolCallId : undefined;
  const next = withActivity(c, { phase: "tool", toolName, toolDetail: detail });
  // Only push a node on start; an update refines the activity strip but the node
  // is already present.
  if (!isStart) return next;
  const node: LiveTraceNode = {
    kind: "tool",
    toolName: toolName ?? undefined,
    label: detail ?? undefined,
    args,
    startedAt: new Date().toISOString(),
    toolCallId
  };
  return { ...next, liveTrace: [...next.liveTrace, node] };
}

// A pi tool_execution_end frame: finalize the matching still-open tool node and
// fall back to "thinking" until the next signal refines the phase.
export function reduceToolEnd(
  c: ConversationState,
  payload: Record<string, unknown>
): ConversationState {
  const toolCallId = typeof payload.toolCallId === "string" ? payload.toolCallId : undefined;
  const result = "result" in payload ? payload.result : undefined;
  const resultSummary = truncate(stringifyResult(result), TRACE_RESULT_MAX) || undefined;
  const isError = payload.isError === true;
  // Finalize the matching still-open tool node: prefer the toolCallId match,
  // else the last tool node without a durationMs.
  let targetIdx = -1;
  for (let i = c.liveTrace.length - 1; i >= 0; i -= 1) {
    const n = c.liveTrace[i];
    if (n.kind !== "tool") continue;
    if (toolCallId) {
      if (n.toolCallId === toolCallId) {
        targetIdx = i;
        break;
      }
    } else if (n.durationMs === undefined) {
      targetIdx = i;
      break;
    }
  }
  let liveTrace = c.liveTrace;
  if (targetIdx !== -1) {
    const node = c.liveTrace[targetIdx];
    const durationMs = node.startedAt
      ? Math.max(0, Date.now() - new Date(node.startedAt).getTime())
      : undefined;
    const finalized: LiveTraceNode = {
      ...node,
      resultSummary,
      isError,
      durationMs,
      diff: node.diff ?? diffFromToolArgs(node.toolName ?? null, node.args)
    };
    liveTrace = c.liveTrace.map((n, i) => (i === targetIdx ? finalized : n));
  }
  const next =
    c.activity.phase === "tool"
      ? withActivity(c, { phase: "thinking", toolName: null, toolDetail: null })
      : c;
  return { ...next, liveTrace };
}

// Assistant is assembling a tool call or bracketing a text block (or a turn
// lifecycle "start" event): mark the turn active without a specific tool label,
// but only when currently idle.
export function reduceThinkingWhenIdle(c: ConversationState): ConversationState {
  return c.activity.phase === "idle" ? withActivity(c, { phase: "thinking" }) : c;
}

// Turn-start family from system.status: also lifts a "retrying" turn back to
// "thinking".
export function reduceThinkingWhenIdleOrRetrying(c: ConversationState): ConversationState {
  return c.activity.phase === "idle" || c.activity.phase === "retrying"
    ? withActivity(c, { phase: "thinking" })
    : c;
}

export function reduceCompactionStart(c: ConversationState): ConversationState {
  return withActivity(c, { phase: "compacting" });
}

export function reduceCompactionEnd(c: ConversationState): ConversationState {
  return c.activity.phase === "compacting" ? withActivity(c, { phase: "thinking" }) : c;
}

export function reduceRetryStart(c: ConversationState): ConversationState {
  return withActivity(c, { phase: "retrying" });
}

export function reduceRetryEnd(c: ConversationState): ConversationState {
  return c.activity.phase === "retrying" ? withActivity(c, { phase: "thinking" }) : c;
}

// A model request went on the wire: count it and mark one more in-flight.
export function reduceProxyStarted(c: ConversationState): ConversationState {
  return withActivity(c, {
    phase: c.activity.phase === "idle" ? "thinking" : c.activity.phase,
    requestCount: c.activity.requestCount + 1,
    inflight: c.activity.inflight + 1
  });
}

// A model request completed or failed: one fewer in-flight.
export function reduceProxySettled(c: ConversationState): ConversationState {
  return withActivity(c, { inflight: Math.max(0, c.activity.inflight - 1) });
}

// Read a text_delta's fields with the provider's defaults applied. Returns null
// when the frame should be ignored (non-text delta or empty text).
export function readAgentDelta(
  payload: Record<string, unknown>
): { id: string; text: string; deltaType: string } | null {
  const text = typeof payload.text === "string" ? payload.text : "";
  const id = typeof payload.message_id === "string" ? payload.message_id : "assistant_pending";
  const deltaType = typeof payload.delta_type === "string" ? payload.delta_type : "text_delta";
  if (deltaType !== "text_delta") return null;
  if (!text) return null;
  return { id, text, deltaType };
}

// Re-export isRecord so the provider can import event helpers from one module.
export { isRecord };

