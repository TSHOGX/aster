"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState
} from "react";
import { toast } from "sonner";

import {
  createSession as apiCreateSession,
  destroySession as apiDestroySession,
  fetchArtifacts,
  fetchDeploymentCapabilities,
  fetchHealth,
  fetchMe,
  fetchMessages,
  fetchNotifications,
  fetchSession,
  fetchSessions,
  interruptSession as apiInterruptSession,
  logout as apiLogout,
  markNotificationRead as apiMarkNotificationRead,
  postMessage as apiPostMessage,
  postMessageFeedback,
  renameSession as apiRenameSession
} from "@/lib/api";
import { reduceSessionEvent } from "@/lib/session-events";
import { buildEventsStreamUrl } from "@/lib/ws";
import type {
  ApiArtifact,
  ApiNotification,
  DeploymentCapabilities,
  ApiMessage,
  ApiSession,
  ConnectionStatus,
  HarnessEvent,
  SessionMode,
  SessionStatus,
  StreamLine,
  TraceDiff,
  TraceNode
} from "@/lib/types";

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

type ConversationState = {
  messages: ApiMessage[];
  streaming: Array<{ id: string; text: string; deltaType?: string }>;
  stream: StreamLine[];
  liveTrace: LiveTraceNode[];
  activity: ConversationActivity;
  loading: boolean;
};

// A live trace node carries the tool call id (when pi provides one) so the
// matching tool_execution_end can finalize the right node even when tools run
// concurrently. The id is internal and dropped when rendered as a TraceNode.
type LiveTraceNode = TraceNode & { toolCallId?: string };

type AuthState = {
  checked: boolean;
  required: boolean;
  userId: string | null;
};

type HarnessState = {
  ready: boolean;
  bootError: string | null;
  connection: ConnectionStatus;
  capabilities: DeploymentCapabilities | null;
  sessions: ApiSession[];
  selectedId: string | null;
  conversations: Record<string, ConversationState>;
  artifacts: Record<string, ApiArtifact[]>;
  notifications: ApiNotification[];
  auth: AuthState;
};

type HarnessApi = {
  state: HarnessState;
  selectSession: (id: string | null) => void;
  createSession: (mode: SessionMode) => Promise<{ ok: boolean; error?: string; session?: ApiSession }>;
  destroySession: (id: string) => Promise<{ ok: boolean; error?: string }>;
  renameSession: (id: string, title: string) => Promise<{ ok: boolean; error?: string }>;
  interruptSession: (id: string) => Promise<{ ok: boolean; error?: string }>;
  sendMessage: (id: string, content: string) => Promise<{ ok: boolean; error?: string }>;
  submitFeedback: (
    sessionId: string,
    messageId: number,
    rating: "up" | "down" | null
  ) => Promise<{ ok: boolean; error?: string }>;
  refresh: () => Promise<void>;
  onLogin: (userId: string) => void;
  logout: () => Promise<void>;
  markNotificationRead: (id: string) => Promise<void>;
};

const HarnessContext = createContext<HarnessApi | null>(null);

const initialState: HarnessState = {
  ready: false,
  bootError: null,
  connection: "connecting",
  capabilities: null,
  sessions: [],
  selectedId: null,
  conversations: {},
  artifacts: {},
  notifications: [],
  auth: { checked: false, required: false, userId: null }
};

const RUNNING_STATUSES = new Set<SessionStatus>(["running_active"]);
const MESSAGE_POLL_INTERVAL_MS = 1000;
const MESSAGE_POLL_TIMEOUT_MS = 120_000;
const SSE_TYPED_EVENT_TYPES = [
  "session.created",
  "session.running_active",
  "session.running_idle",
  "session.checkpointing",
  "session.checkpointed",
  "session.failed",
  "session.destroyed",
  "session.updated",
  "message.created",
  "agent.message",
  "agent.delta",
  "agent.output",
  "system.status",
  "session.error",
  "generation.error",
  "session.checkpoint_retired",
  "session.restore_fallback_retired",
  "artifact.updated",
  "artifact.deleted",
  "ack_turn_started",
  "emit_output",
  "ack_turn_completed",
  "proxy.request.started",
  "proxy.request.completed",
  "proxy.request.failed",
  "replay_gap",
  "error",
  "notification.created"
] as const;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function readSession(payload: unknown): ApiSession | null {
  if (!isRecord(payload)) return null;
  const id = typeof payload.id === "string" ? payload.id : null;
  if (!id) return null;
  return payload as unknown as ApiSession;
}

function readArtifact(payload: unknown): ApiArtifact | null {
  if (!isRecord(payload)) return null;
  const sessionId = typeof payload.session_id === "string" ? payload.session_id : null;
  const path = typeof payload.path === "string" ? payload.path : null;
  if (!sessionId || !path) return null;
  return payload as unknown as ApiArtifact;
}

function newId() {
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`;
}

function idleActivity(): ConversationActivity {
  return { phase: "idle", startedAt: null, requestCount: 0, inflight: 0, toolName: null, toolDetail: null };
}

function emptyConvo(): ConversationState {
  return {
    messages: [],
    streaming: [],
    stream: [],
    liveTrace: [] as LiveTraceNode[],
    activity: idleActivity(),
    loading: false
  };
}

// Monotonic clock for turn timers. Date.now would be fine, but performance.now
// is immune to wall-clock adjustments during a long-running turn.
function nowMs(): number {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

function isActiveStatus(status: SessionStatus) {
  return RUNNING_STATUSES.has(status);
}

function appendStreaming(
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

function pruneCommittedStreaming(
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

// Derive a short human label from a pi tool-call input object, e.g. the file
// path being read or the command being run. Falls back to null when nothing
// obviously label-worthy is present.
function toolDetailFromInput(input: unknown): string | null {
  if (!isRecord(input)) return null;
  for (const key of ["path", "file_path", "command", "cmd", "pattern", "query", "url", "name"]) {
    const value = input[key];
    if (typeof value === "string" && value.trim()) {
      const trimmed = value.trim();
      return trimmed.length > 80 ? `${trimmed.slice(0, 79)}…` : trimmed;
    }
  }
  return null;
}

// pi emits `toolName`/`args` on tool lifecycle events; older frames used
// `name`/`input`. Read the modern shape first and fall back so labels populate
// regardless of which the backend sends.
function readToolName(payload: Record<string, unknown>): string | null {
  const modern = payload.toolName;
  if (typeof modern === "string" && modern.trim()) return modern;
  const legacy = payload.name;
  return typeof legacy === "string" && legacy.trim() ? legacy : null;
}

function readToolArgs(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const modern = payload.args;
  if (isRecord(modern)) return modern;
  const legacy = payload.input;
  return isRecord(legacy) ? legacy : undefined;
}

const TRACE_RESULT_MAX = 2000;

function truncate(value: string, max: number): string {
  return value.length > max ? `${value.slice(0, max - 1)}…` : value;
}

function stringifyResult(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

// Build a diff preview from a write/edit tool's arguments so the trace node can
// render the change inline. Returns undefined for non-mutating tools.
function diffFromToolArgs(
  toolName: string | null,
  args: Record<string, unknown> | undefined
): TraceDiff | undefined {
  if (!toolName || !args) return undefined;
  const name = toolName.toLowerCase();
  const path =
    typeof args.path === "string"
      ? args.path
      : typeof args.file_path === "string"
      ? args.file_path
      : undefined;
  if (name === "write") {
    const content = typeof args.content === "string" ? args.content : undefined;
    if (content === undefined) return undefined;
    return { path, content };
  }
  if (name === "edit") {
    const rawEdits = Array.isArray(args.edits) ? args.edits : null;
    if (rawEdits) {
      const edits = rawEdits
        .filter(isRecord)
        .map((e) => ({
          oldText: typeof e.oldText === "string" ? e.oldText : typeof e.old_text === "string" ? e.old_text : "",
          newText: typeof e.newText === "string" ? e.newText : typeof e.new_text === "string" ? e.new_text : ""
        }));
      if (edits.length > 0) return { path, edits };
    }
    const oldText =
      typeof args.oldText === "string" ? args.oldText : typeof args.old_text === "string" ? args.old_text : null;
    const newText =
      typeof args.newText === "string" ? args.newText : typeof args.new_text === "string" ? args.new_text : null;
    if (oldText !== null || newText !== null) {
      return { path, edits: [{ oldText: oldText ?? "", newText: newText ?? "" }] };
    }
  }
  return undefined;
}

function withActivity(
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

export function HarnessProvider({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<HarnessState>(initialState);
  const stateRef = useRef(state);

  const aliveRef = useRef(true);
  const pollingRef = useRef<Set<string>>(new Set());
  const connectedOnceRef = useRef(false);
  const lastEventIDRef = useRef<number | null>(null);

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  const ensureConvo = useCallback((convos: HarnessState["conversations"], id: string) => {
    if (convos[id]) return convos;
    return { ...convos, [id]: emptyConvo() };
  }, []);

  const upsertConvo = useCallback(
    (id: string, patch: (prev: ConversationState) => ConversationState) => {
      setState((prev) => {
        const current = prev.conversations[id] ?? emptyConvo();
        return { ...prev, conversations: { ...prev.conversations, [id]: patch(current) } };
      });
    },
    []
  );

  const loadSessionDetails = useCallback(
    (id: string, skipLoaded = false) => {
      const convo = stateRef.current.conversations[id];
      if (skipLoaded && convo && convo.messages.length > 0) return;
      upsertConvo(id, (c) => ({ ...c, loading: true }));
      void fetchMessages(id).then((res) => {
        upsertConvo(id, (c) => ({
          ...c,
          loading: false,
          messages: res.ok ? res.data.messages ?? [] : c.messages
        }));
      });
      void fetchArtifacts(id).then((res) => {
        if (!res.ok) return;
        const list = res.data.artifacts ?? [];
        setState((p) => ({ ...p, artifacts: { ...p.artifacts, [id]: list } }));
      });
    },
    [upsertConvo]
  );

  const pollConversation = useCallback(
    async (id: string, afterMessageId?: number) => {
      if (pollingRef.current.has(id)) return;
      pollingRef.current.add(id);
      const deadline = Date.now() + MESSAGE_POLL_TIMEOUT_MS;

      try {
        while (aliveRef.current && Date.now() < deadline) {
          const [messagesRes, sessionRes, artifactsRes] = await Promise.all([
            fetchMessages(id),
            fetchSession(id),
            fetchArtifacts(id)
          ]);
          let sawAssistant = false;
          let active = true;

          if (sessionRes.ok) {
            const session = sessionRes.data;
            active = isActiveStatus(session.status);
            setState((p) => ({
              ...p,
              sessions: p.sessions.some((s) => s.id === session.id)
                ? p.sessions.map((s) => (s.id === session.id ? session : s))
                : [session, ...p.sessions],
              conversations: ensureConvo(p.conversations, session.id)
            }));
            if (!active) {
              upsertConvo(id, (c) => withActivity({ ...c, streaming: [] }, { phase: "idle" }));
            }
          }

          if (messagesRes.ok) {
            const messages = messagesRes.data.messages ?? [];
            const committedAssistantMessages = messages.filter(
              (m) => m.role === "assistant" && (afterMessageId === undefined || m.id > afterMessageId)
            );
            sawAssistant = committedAssistantMessages.length > 0;
            upsertConvo(id, (c) => ({
              ...c,
              loading: false,
              streaming: active ? pruneCommittedStreaming(c.streaming, committedAssistantMessages) : [],
              messages
            }));
          }

          if (artifactsRes.ok) {
            const list = artifactsRes.data.artifacts ?? [];
            setState((p) => ({ ...p, artifacts: { ...p.artifacts, [id]: list } }));
          }

          if (!active || (sawAssistant && !sessionRes.ok)) return;
          await delay(MESSAGE_POLL_INTERVAL_MS);
        }
      } finally {
        pollingRef.current.delete(id);
      }
    },
    [ensureConvo, upsertConvo]
  );

  const refreshNotifications = useCallback(async () => {
    const res = await fetchNotifications();
    if (!res.ok) return;
    setState((p) => ({ ...p, notifications: res.data.notifications ?? [] }));
  }, []);

  const handleEvent = useCallback(
    (event: HarnessEvent) => {
      const sessionId = event.session_id;
      const time = event.time ?? new Date().toISOString();
      const applySessionEvent = () => {
        const preview = reduceSessionEvent(
          { sessions: stateRef.current.sessions, conversations: stateRef.current.conversations },
          event,
          { emptyConversation: emptyConvo, time }
        );
        if (!preview.handled) return false;
        for (const notification of preview.notifications) {
          if (notification.level === "error") {
            toast.error(notification.message, { duration: 6000 });
          }
        }
        setState((p) => {
          const next = reduceSessionEvent(
            { sessions: p.sessions, conversations: p.conversations },
            event,
            { emptyConversation: emptyConvo, time }
          );
          // The reducer clears streaming on turn/terminal events but is generic
          // over activity; mirror that by parking activity back to idle for the
          // affected session unless the turn is still active.
          let conversations = next.conversations;
          if (sessionId && conversations[sessionId]) {
            const stillActive = event.type === "session.running_active";
            if (!stillActive) {
              conversations = {
                ...conversations,
                [sessionId]: withActivity(conversations[sessionId], { phase: "idle" })
              };
            }
          }
          return { ...p, sessions: next.sessions, conversations };
        });
        return true;
      };
      switch (event.type) {
        case "session.created": {
          const sess = readSession(event.payload);
          if (!sess) return;
          setState((p) => ({
            ...p,
            sessions: [sess, ...p.sessions.filter((s) => s.id !== sess.id)],
            conversations: ensureConvo(p.conversations, sess.id)
          }));
          return;
        }
        case "session.running_active":
        case "session.running_idle":
        case "session.checkpointing":
        case "session.checkpointed":
        case "session.failed":
        case "session.destroyed": {
          applySessionEvent();
          return;
        }
        case "session.updated": {
          const sess = readSession(event.payload);
          if (!sess) return;
          setState((p) => ({
            ...p,
            sessions: p.sessions.map((s) => (s.id === sess.id ? { ...s, ...sess } : s))
          }));
          return;
        }
        case "message.created": {
          if (!sessionId) return;
          const msg = event.payload as ApiMessage | undefined;
          if (!msg || typeof msg.id !== "number") return;
          upsertConvo(sessionId, (c) => ({
            ...c,
            // A new user turn begins here: drop any live trace left from before.
            liveTrace: msg.role === "user" ? [] : c.liveTrace,
            messages: c.messages.some((m) => m.id === msg.id) ? c.messages : [...c.messages, msg]
          }));
          return;
        }
        case "agent.message": {
          if (!sessionId) return;
          const msg = event.payload as ApiMessage | undefined;
          if (!msg || typeof msg.id !== "number") return;
          upsertConvo(sessionId, (c) => ({
            ...c,
            streaming: pruneCommittedStreaming(c.streaming, [msg]),
            // The committed assistant message renders its own server trace; clear
            // the live nodes so they don't duplicate below it.
            liveTrace: [],
            messages: c.messages.some((m) => m.id === msg.id) ? c.messages : [...c.messages, msg]
          }));
          return;
        }
        case "agent.delta": {
          if (!sessionId || !isRecord(event.payload)) return;
          const text = typeof event.payload.text === "string" ? event.payload.text : "";
          const id = typeof event.payload.message_id === "string" ? event.payload.message_id : "assistant_pending";
          const deltaType = typeof event.payload.delta_type === "string" ? event.payload.delta_type : "text_delta";
          if (deltaType !== "text_delta") return;
          if (!text) return;
          upsertConvo(sessionId, (c) =>
            withActivity({ ...c, streaming: appendStreaming(c.streaming, id, text, deltaType) }, {
              phase: "streaming"
            })
          );
          return;
        }
        case "agent.output": {
          if (!sessionId || !isRecord(event.payload)) return;
          const payload = event.payload;
          const piType = typeof payload.type === "string" ? payload.type : "";

          // pi lifecycle events arrive here as their raw event map (no `line`
          // field). Drive the activity state machine from tool calls so the UI
          // can show "Running bash" / "Reading README.md" during the gap
          // between text tokens where it previously looked frozen.
          if (piType === "tool_execution_start" || piType === "tool_execution_update") {
            const toolName = readToolName(payload);
            const args = readToolArgs(payload);
            const detail = toolDetailFromInput(args ?? payload.input);
            const toolCallId = typeof payload.toolCallId === "string" ? payload.toolCallId : undefined;
            upsertConvo(sessionId, (c) => {
              const next = withActivity(c, {
                phase: "tool",
                toolName,
                toolDetail: detail
              });
              // Only push a node on start; an update refines the activity strip
              // but the node is already present.
              if (piType !== "tool_execution_start") return next;
              const node: LiveTraceNode = {
                kind: "tool",
                toolName: toolName ?? undefined,
                label: detail ?? undefined,
                args,
                startedAt: new Date().toISOString(),
                toolCallId
              };
              return { ...next, liveTrace: [...next.liveTrace, node] };
            });
            return;
          }
          if (piType === "tool_execution_end") {
            const toolCallId = typeof payload.toolCallId === "string" ? payload.toolCallId : undefined;
            const result = "result" in payload ? payload.result : undefined;
            const resultSummary = truncate(stringifyResult(result), TRACE_RESULT_MAX) || undefined;
            const isError = payload.isError === true;
            upsertConvo(sessionId, (c) => {
              // Finalize the matching still-open tool node: prefer the toolCallId
              // match, else the last tool node without a durationMs.
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
              // Tool finished; fall back to thinking until the next signal (more
              // tools, or text deltas) refines the phase.
              const next =
                c.activity.phase === "tool"
                  ? withActivity(c, { phase: "thinking", toolName: null, toolDetail: null })
                  : c;
              return { ...next, liveTrace };
            });
            return;
          }
          if (
            piType === "toolcall_start" ||
            piType === "toolcall_delta" ||
            piType === "toolcall_end" ||
            piType === "text_start" ||
            piType === "text_end"
          ) {
            // Assistant is assembling a tool call or bracketing a text block:
            // keep the turn marked active without a specific tool label.
            upsertConvo(sessionId, (c) =>
              c.activity.phase === "idle" ? withActivity(c, { phase: "thinking" }) : c
            );
            return;
          }

          const stream = typeof payload.stream === "string" ? payload.stream : "stdout";
          const line = typeof payload.line === "string" ? payload.line : "";
          if (!line) return;
          const entry: StreamLine = {
            id: newId(),
            session_id: sessionId,
            stream,
            line,
            time
          };
          upsertConvo(sessionId, (c) => ({ ...c, stream: [...c.stream, entry].slice(-400) }));
          return;
        }
        case "proxy.request.started": {
          if (!sessionId) return;
          // A model request is on the wire. Count it and mark the request as
          // in-flight so the activity strip can show "waiting on model".
          upsertConvo(sessionId, (c) =>
            withActivity(c, {
              phase: c.activity.phase === "idle" ? "thinking" : c.activity.phase,
              requestCount: c.activity.requestCount + 1,
              inflight: c.activity.inflight + 1
            })
          );
          return;
        }
        case "proxy.request.completed":
        case "proxy.request.failed": {
          if (!sessionId) return;
          upsertConvo(sessionId, (c) =>
            withActivity(c, { inflight: Math.max(0, c.activity.inflight - 1) })
          );
          return;
        }
        case "system.status": {
          if (!sessionId || !isRecord(event.payload)) return;
          const payload = event.payload;
          const piType = typeof payload.type === "string" ? payload.type : "";

          // Drive the activity state machine from turn lifecycle events instead
          // of firing a toast for each one (they were noisy and left no trace).
          switch (piType) {
            case "turn_start":
            case "agent_start":
            case "message_start":
            case "queue_update":
              upsertConvo(sessionId, (c) =>
                c.activity.phase === "idle" || c.activity.phase === "retrying"
                  ? withActivity(c, { phase: "thinking" })
                  : c
              );
              return;
            case "compaction_start":
              upsertConvo(sessionId, (c) => withActivity(c, { phase: "compacting" }));
              return;
            case "compaction_end":
              upsertConvo(sessionId, (c) =>
                c.activity.phase === "compacting" ? withActivity(c, { phase: "thinking" }) : c
              );
              return;
            case "auto_retry_start":
            case "auto_retry":
              upsertConvo(sessionId, (c) => withActivity(c, { phase: "retrying" }));
              return;
            case "auto_retry_end":
              upsertConvo(sessionId, (c) =>
                c.activity.phase === "retrying" ? withActivity(c, { phase: "thinking" }) : c
              );
              return;
            default:
              break;
          }

          if (process.env.NODE_ENV === "development") {
            const line = typeof payload.line === "string" ? payload.line : piType;
            if (line) console.log(`[System] ${line}`);
          }
          return;
        }
        case "session.error": {
          applySessionEvent();
          return;
        }
        case "generation.error": {
          applySessionEvent();
          return;
        }
        case "session.checkpoint_retired":
        case "session.restore_fallback_retired": {
          applySessionEvent();
          return;
        }
        case "ack_turn_completed": {
          applySessionEvent();
          return;
        }
        case "artifact.updated": {
          const artifact = readArtifact(event.payload);
          if (!artifact) return;
          setState((p) => {
            const list = p.artifacts[artifact.session_id] ?? [];
            const next = [
              artifact,
              ...list.filter((a) => a.path !== artifact.path)
            ];
            return { ...p, artifacts: { ...p.artifacts, [artifact.session_id]: next } };
          });
          return;
        }
        case "artifact.deleted": {
          if (!isRecord(event.payload)) return;
          const deletedSessionId =
            typeof event.payload.session_id === "string" ? event.payload.session_id : sessionId;
          const path = typeof event.payload.path === "string" ? event.payload.path : null;
          if (!deletedSessionId || !path) return;
          setState((p) => {
            const list = p.artifacts[deletedSessionId] ?? [];
            const next = list.filter((a) => a.path !== path && !a.path.startsWith(`${path}/`));
            return { ...p, artifacts: { ...p.artifacts, [deletedSessionId]: next } };
          });
          return;
        }
        case "notification.created": {
          void refreshNotifications();
          toast.info("New notification", { duration: 3000 });
          return;
        }
        default:
          return;
      }
    },
    [ensureConvo, upsertConvo, refreshNotifications]
  );

  const handleEventRef = useRef(handleEvent);
  useEffect(() => {
    handleEventRef.current = handleEvent;
  }, [handleEvent]);

  const handleUnauthorized = useCallback(() => {
    aliveRef.current = false;
    setState((p) => ({
      ...p,
      auth: { checked: true, required: true, userId: null },
      sessions: [],
      conversations: {},
      artifacts: {},
      selectedId: null,
      connection: "down"
    }));
  }, []);

  const refresh = useCallback(async () => {
    const health = await fetchHealth();
    if (!health.ok) {
      setState((p) => ({ ...p, ready: true, bootError: health.error, sessions: [] }));
      return;
    }
    const sessions = await fetchSessions();
    if (!sessions.ok) {
      if (sessions.status === 401) {
        handleUnauthorized();
        return;
      }
      setState((p) => ({ ...p, ready: true, bootError: sessions.error, sessions: [] }));
      return;
    }
    const capabilities = await fetchDeploymentCapabilities();
    const list = sessions.data.sessions ?? [];
    const currentSelected = stateRef.current.selectedId;
    const selectedId = currentSelected && list.some((s) => s.id === currentSelected) ? currentSelected : list[0]?.id ?? null;
    setState((p) => {
      const conversations = { ...p.conversations };
      for (const s of list) {
        if (!conversations[s.id]) conversations[s.id] = emptyConvo();
      }
      return {
        ...p,
        ready: true,
        bootError: capabilities.ok ? null : capabilities.error,
        capabilities: capabilities.ok ? capabilities.data : p.capabilities,
        sessions: list,
        selectedId,
        conversations
      };
    });
    if (selectedId) {
      loadSessionDetails(selectedId, true);
      const selected = list.find((s) => s.id === selectedId);
      if (selected && isActiveStatus(selected.status)) {
        void pollConversation(selectedId);
      }
    }
    void refreshNotifications();
  }, [loadSessionDetails, pollConversation, handleUnauthorized, refreshNotifications]);

  // Determine auth state on boot before connecting any streams.
  const authReady = state.auth.checked && (!state.auth.required || state.auth.userId !== null);

  useEffect(() => {
    let cancelled = false;
    void fetchMe().then((res) => {
      if (cancelled) return;
      if (!res.ok) {
        // /api/me should never 401; treat failure as backend-unreachable.
        setState((p) => ({ ...p, auth: { checked: true, required: false, userId: null } }));
        return;
      }
      setState((p) => ({
        ...p,
        auth: {
          checked: true,
          required: res.data.auth_required,
          userId: res.data.user_id || null
        }
      }));
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const onLogin = useCallback((userId: string) => {
    aliveRef.current = true;
    lastEventIDRef.current = null;
    setState((p) => ({
      ...p,
      auth: { checked: true, required: true, userId },
      connection: "connecting"
    }));
  }, []);

  const logout = useCallback(async () => {
    await apiLogout();
    handleUnauthorized();
  }, [handleUnauthorized]);

  useEffect(() => {
    if (!authReady) return;
    aliveRef.current = true;
    let cleanedUp = false;
    connectedOnceRef.current = false;

    let source: EventSource;
    try {
      source = new EventSource(buildEventsStreamUrl(undefined, lastEventIDRef.current));
    } catch {
      const failureTimer = setTimeout(() => {
        if (!cleanedUp) {
          setState((p) => ({ ...p, connection: "down" }));
        }
      }, 0);
      return () => {
        cleanedUp = true;
        aliveRef.current = false;
        clearTimeout(failureTimer);
      };
    }

    source.onopen = () => {
      if (cleanedUp) return;
      const wasConnected = connectedOnceRef.current;
      connectedOnceRef.current = true;
      setState((p) => ({ ...p, connection: "live" }));
      if (wasConnected) {
        void refresh();
      }
    };

    const handleSourceEvent = (ev: MessageEvent) => {
      try {
        const data = JSON.parse(typeof ev.data === "string" ? ev.data : "");
        const eventID =
          data && typeof data.event_id === "number"
            ? data.event_id
            : Number.parseInt(ev.lastEventId, 10);
        if (Number.isFinite(eventID) && eventID > 0) {
          lastEventIDRef.current = eventID;
        }
        if (data && data.type === "replay_gap") {
          void refresh();
          return;
        }
        if (data && typeof data.type === "string") {
          handleEventRef.current(data as HarnessEvent);
        }
      } catch {
        // ignore malformed frames
      }
    };
    source.onmessage = handleSourceEvent;
    for (const eventType of SSE_TYPED_EVENT_TYPES) {
      source.addEventListener(eventType, handleSourceEvent);
    }

    source.onerror = () => {
      if (cleanedUp) return;
      if (source.readyState === EventSource.CLOSED) {
        setState((p) => ({ ...p, connection: "down" }));
        return;
      }
      setState((p) => ({ ...p, connection: connectedOnceRef.current ? "reconnecting" : "connecting" }));
    };

    const bootTimer = setTimeout(() => {
      void refresh();
    }, 0);

    return () => {
      cleanedUp = true;
      aliveRef.current = false;
      clearTimeout(bootTimer);
      for (const eventType of SSE_TYPED_EVENT_TYPES) {
        source.removeEventListener(eventType, handleSourceEvent);
      }
      source.close();
    };
  }, [refresh, authReady]);

  const selectSession = useCallback(
    (id: string | null) => {
      setState((p) => ({ ...p, selectedId: id }));
      if (!id) return;
      loadSessionDetails(id, true);
      const session = stateRef.current.sessions.find((s) => s.id === id);
      if (session && isActiveStatus(session.status)) {
        void pollConversation(id);
      }
    },
    [loadSessionDetails, pollConversation]
  );

  const createSession = useCallback(
    async (mode: SessionMode) => {
      const res = await apiCreateSession(mode);
      if (!res.ok) return { ok: false as const, error: res.error };
      const session = res.data;
      setState((p) => ({
        ...p,
        sessions: [session, ...p.sessions.filter((s) => s.id !== session.id)],
        selectedId: session.id,
        conversations: { ...p.conversations, [session.id]: emptyConvo() }
      }));
      return { ok: true as const, session };
    },
    []
  );

  const destroySession = useCallback(async (id: string) => {
    const res = await apiDestroySession(id);
    if (!res.ok) return { ok: false as const, error: res.error };
    const now = new Date().toISOString();
    setState((p) => ({
      ...p,
      sessions: p.sessions.map((s) =>
        s.id === id
          ? {
              ...s,
              status: res.data.status,
              updated_at: now,
              ended_at: s.ended_at ?? now,
              restore_ms: null
            }
          : s
      )
    }));
    return { ok: true as const };
  }, []);

  const renameSession = useCallback(async (id: string, title: string) => {
    const trimmed = title.trim();
    if (!trimmed) return { ok: false as const, error: "Title is required" };
    const previous = stateRef.current.sessions.find((s) => s.id === id)?.title;
    // Optimistic: reflect the new title immediately, roll back on failure.
    setState((p) => ({
      ...p,
      sessions: p.sessions.map((s) => (s.id === id ? { ...s, title: trimmed } : s))
    }));
    const res = await apiRenameSession(id, trimmed);
    if (!res.ok) {
      setState((p) => ({
        ...p,
        sessions: p.sessions.map((s) => (s.id === id ? { ...s, title: previous } : s))
      }));
      return { ok: false as const, error: res.error };
    }
    const session = res.data;
    setState((p) => ({
      ...p,
      sessions: p.sessions.map((s) => (s.id === session.id ? { ...s, ...session } : s))
    }));
    return { ok: true as const };
  }, []);

  const interruptSession = useCallback(async (id: string) => {
    const res = await apiInterruptSession(id);
    if (!res.ok) return { ok: false as const, error: res.error };
    return { ok: true as const };
  }, []);

  const sendMessage = useCallback(
    async (id: string, content: string) => {
      const res = await apiPostMessage(id, content);
      if (!res.ok) return { ok: false as const, error: res.error };
      // server emits message.created via WS; merge here for instant feedback
      const msg = res.data.message;
      if (msg) {
        upsertConvo(id, (c) => ({
          ...c,
          messages: c.messages.some((m) => m.id === msg.id) ? c.messages : [...c.messages, msg]
        }));
      }
      setState((p) => ({
        ...p,
        sessions: p.sessions.map((s) =>
          s.id === id ? { ...s, status: res.data.status, updated_at: new Date().toISOString() } : s
        )
      }));
      void pollConversation(id, msg?.id);
      return { ok: true as const };
    },
    [pollConversation, upsertConvo]
  );

  const submitFeedback = useCallback(
    async (sessionId: string, messageId: number, rating: "up" | "down" | null) => {
      const previous =
        stateRef.current.conversations[sessionId]?.messages.find((m) => m.id === messageId)?.feedback ?? "";
      const optimistic: ApiMessage["feedback"] = rating ?? "";
      // Optimistic: reflect the new rating immediately, roll back on failure.
      upsertConvo(sessionId, (c) => ({
        ...c,
        messages: c.messages.map((m) => (m.id === messageId ? { ...m, feedback: optimistic } : m))
      }));
      const res = await postMessageFeedback(sessionId, messageId, rating);
      if (!res.ok) {
        upsertConvo(sessionId, (c) => ({
          ...c,
          messages: c.messages.map((m) => (m.id === messageId ? { ...m, feedback: previous } : m))
        }));
        toast.error(res.error || "Failed to submit feedback");
        return { ok: false as const, error: res.error };
      }
      // Reconcile with the server's canonical value.
      const settled = (res.data.feedback === "up" || res.data.feedback === "down"
        ? res.data.feedback
        : "") as ApiMessage["feedback"];
      upsertConvo(sessionId, (c) => ({
        ...c,
        messages: c.messages.map((m) => (m.id === messageId ? { ...m, feedback: settled } : m))
      }));
      return { ok: true as const };
    },
    [upsertConvo]
  );

  const markNotificationRead = useCallback(async (id: string) => {
    setState((p) => ({
      ...p,
      notifications: p.notifications.map((n) => (n.id === id ? { ...n, read: true } : n))
    }));
    await apiMarkNotificationRead(id);
  }, []);

  const value = useMemo<HarnessApi>(
    () => ({
      state,
      selectSession,
      createSession,
      destroySession,
      renameSession,
      interruptSession,
      sendMessage,
      submitFeedback,
      refresh,
      onLogin,
      logout,
      markNotificationRead
    }),
    [
      state,
      selectSession,
      createSession,
      destroySession,
      renameSession,
      interruptSession,
      sendMessage,
      submitFeedback,
      refresh,
      onLogin,
      logout,
      markNotificationRead
    ]
  );

  return <HarnessContext.Provider value={value}>{children}</HarnessContext.Provider>;
}

export function useHarness() {
  const ctx = useContext(HarnessContext);
  if (!ctx) throw new Error("useHarness must be used inside HarnessProvider");
  return ctx;
}

export function useSelectedSession() {
  const { state } = useHarness();
  if (!state.selectedId) return null;
  return state.sessions.find((s) => s.id === state.selectedId) ?? null;
}

export function useConversation(sessionId: string | null) {
  const { state } = useHarness();
  if (!sessionId) return emptyConvo();
  return state.conversations[sessionId] ?? emptyConvo();
}

export function useActivity(sessionId: string | null): ConversationActivity {
  const { state } = useHarness();
  if (!sessionId) return idleActivity();
  return state.conversations[sessionId]?.activity ?? idleActivity();
}

export function useArtifacts(sessionId: string | null) {
  const { state } = useHarness();
  if (!sessionId) return [];
  return state.artifacts[sessionId] ?? [];
}

export function useNotifications() {
  const { state, markNotificationRead } = useHarness();
  return { notifications: state.notifications, markNotificationRead };
}
