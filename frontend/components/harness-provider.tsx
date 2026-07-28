"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore
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
import { isRecord } from "@/lib/guards";
import {
  emptyConvo,
  idleActivity,
  pruneCommittedStreaming,
  readAgentDelta,
  reduceAgentDelta,
  reduceCompactionEnd,
  reduceCompactionStart,
  reduceProxySettled,
  reduceProxyStarted,
  reduceRetryEnd,
  reduceRetryStart,
  reduceThinkingWhenIdle,
  reduceThinkingWhenIdleOrRetrying,
  reduceToolActivity,
  reduceToolEnd,
  withActivity,
  type ConversationActivity,
  type ConversationState
} from "@/lib/harness-events";
import { mergeSessionList, reduceSessionEvent } from "@/lib/session-events";
import { useEventStream } from "@/lib/use-event-stream";
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
  SessionStatus
} from "@/lib/types";

// Activity/conversation types and their pure reducers now live in
// lib/harness-events.ts. Re-export the two public types so existing consumers
// (e.g. ActivityIndicator) keep importing them from this module unchanged.
export type { ActivityPhase, ConversationActivity } from "@/lib/harness-events";

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

// The stable action surface. Identity never changes across the provider's
// lifetime, so destructuring these from useHarness() never causes a re-render.
type HarnessActions = {
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

// A minimal external store. State lives in a mutable ref; setState keeps the
// exact (prev) => next updater contract the reducer code already uses, and
// synchronously notifies subscribers. Selector hooks (useSyncExternalStore)
// then re-render only the components whose selected slice actually changed,
// so a per-token agent.delta no longer re-renders every consumer.
type HarnessStore = {
  getState: () => HarnessState;
  setState: (updater: (prev: HarnessState) => HarnessState) => void;
  subscribe: (listener: () => void) => () => void;
};

type HarnessContextValue = {
  store: HarnessStore;
  actions: HarnessActions;
};

const HarnessContext = createContext<HarnessContextValue | null>(null);

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

// Module-level stable references so selector hooks return a constant identity
// for the "no such conversation / no artifacts" case. Reference equality on
// these prevents an unknown id from churning re-renders every notify.
const EMPTY_CONVO: ConversationState = Object.freeze(emptyConvo()) as ConversationState;
const EMPTY_ACTIVITY: ConversationActivity = Object.freeze(idleActivity()) as ConversationActivity;
const EMPTY_ARTIFACTS: ApiArtifact[] = Object.freeze([]) as unknown as ApiArtifact[];

// Shallow equality for flat objects. Used by the activity selector so that a
// delta which rebuilds the activity object but leaves every field unchanged
// does not re-render ActivityIndicator.
function shallowEqual<T extends Record<string, unknown>>(a: T, b: T): boolean {
  if (Object.is(a, b)) return true;
  if (a == null || b == null) return false;
  const aKeys = Object.keys(a);
  const bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    if (!Object.is(a[key], b[key])) return false;
  }
  return true;
}

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

function isActiveStatus(status: SessionStatus) {
  return RUNNING_STATUSES.has(status);
}

export function HarnessProvider({ children }: { children: React.ReactNode }) {
  // The external store. Domain state no longer lives in React state, so the
  // provider itself does not re-render when it changes; selector hooks (below)
  // subscribe to just the slices they read. A lazy useState initializer builds
  // it exactly once and hands back a stable value (never a ref read during
  // render), so its identity is constant for the provider's lifetime.
  const [store] = useState<HarnessStore>(() => {
    let current = initialState;
    const listeners = new Set<() => void>();
    return {
      getState: () => current,
      setState: (updater) => {
        const next = updater(current);
        if (next === current) return;
        current = next;
        // Synchronous notify: keeps the reducer's "one event → one state"
        // semantics identical to the previous useState behavior.
        for (const listener of listeners) listener();
      },
      subscribe: (listener) => {
        listeners.add(listener);
        return () => {
          listeners.delete(listener);
        };
      }
    };
  });

  // Preserve the (prev) => next contract every existing call site relies on.
  const setState = useCallback(
    (updater: (prev: HarnessState) => HarnessState) => store.setState(updater),
    [store]
  );

  // stateRef.current always resolves to the live store state — no syncing
  // effect needed since store.getState() is authoritative. Memoized so its
  // identity is stable (store is stable), which keeps dependent callbacks stable.
  const stateRef = useMemo(() => ({ get current() { return store.getState(); } }), [store]);

  const aliveRef = useRef(true);
  const pollingRef = useRef<Set<string>>(new Set());
  const lastEventIDRef = useRef<number | null>(null);

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
    [setState]
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
    [upsertConvo, setState, stateRef]
  );

  const pollConversation = useCallback(
    async (id: string, afterMessageId?: number) => {
      if (pollingRef.current.has(id)) return;
      // Polling is now a FALLBACK for when SSE is down. When the stream is
      // "live" it already delivers messages, artifacts, and session status, so
      // the 1s triple-fetch loop is redundant and just adds load + churn.
      // Read through a function so TS doesn't narrow the live getter's value
      // (stateRef.current.connection can change between iterations).
      const streamLive = () => stateRef.current.connection === "live";
      if (streamLive()) return;
      pollingRef.current.add(id);
      const deadline = Date.now() + MESSAGE_POLL_TIMEOUT_MS;

      try {
        while (aliveRef.current && Date.now() < deadline && !streamLive()) {
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
    [ensureConvo, upsertConvo, setState, stateRef]
  );

  const refreshNotifications = useCallback(async () => {
    const res = await fetchNotifications();
    if (!res.ok) return;
    setState((p) => ({ ...p, notifications: res.data.notifications ?? [] }));
  }, [setState]);

  // Re-fetch only the session list, leaving selection and loaded conversations
  // alone. Deliberately narrower than refresh(): callers below run this in the
  // background while the user is reading something.
  const refreshSessions = useCallback(async () => {
    const res = await fetchSessions();
    // Bail silently on failure: a transient error must not empty a healthy list.
    if (!res.ok) return;
    const list = res.data.sessions ?? [];
    setState((p) => {
      const next = mergeSessionList(
        { sessions: p.sessions, conversations: p.conversations },
        list,
        { emptyConversation: emptyConvo }
      );
      return { ...p, sessions: next.sessions, conversations: next.conversations };
    });
  }, [setState]);

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
          const delta = readAgentDelta(event.payload);
          if (!delta) return;
          upsertConvo(sessionId, (c) => reduceAgentDelta(c, delta.id, delta.text, delta.deltaType));
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
            upsertConvo(sessionId, (c) =>
              reduceToolActivity(c, payload, piType === "tool_execution_start")
            );
            return;
          }
          if (piType === "tool_execution_end") {
            upsertConvo(sessionId, (c) => reduceToolEnd(c, payload));
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
            upsertConvo(sessionId, reduceThinkingWhenIdle);
            return;
          }

          // stdout/stderr lines (pi events carrying a `line` field) are not
          // rendered anywhere, so there is nothing to accumulate here.
          return;
        }
        case "proxy.request.started": {
          if (!sessionId) return;
          // A model request is on the wire. Count it and mark the request as
          // in-flight so the activity strip can show "waiting on model".
          upsertConvo(sessionId, reduceProxyStarted);
          return;
        }
        case "proxy.request.completed":
        case "proxy.request.failed": {
          if (!sessionId) return;
          upsertConvo(sessionId, reduceProxySettled);
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
              upsertConvo(sessionId, reduceThinkingWhenIdleOrRetrying);
              return;
            case "compaction_start":
              upsertConvo(sessionId, reduceCompactionStart);
              return;
            case "compaction_end":
              upsertConvo(sessionId, reduceCompactionEnd);
              return;
            case "auto_retry_start":
            case "auto_retry":
              upsertConvo(sessionId, reduceRetryStart);
              return;
            case "auto_retry_end":
              upsertConvo(sessionId, reduceRetryEnd);
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
          // A notification can also mean the caller's session list changed: a
          // share grant notifies the grantee, and that grant is what makes the
          // session visible to them. Without this the sidebar's "Shared with
          // me" group stays empty until a full reload.
          void refreshSessions();
          toast.info("New notification", { duration: 3000 });
          return;
        }
        default:
          return;
      }
    },
    [ensureConvo, upsertConvo, refreshNotifications, refreshSessions, setState, stateRef]
  );


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
  }, [setState]);

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
  }, [loadSessionDetails, pollConversation, handleUnauthorized, refreshNotifications, setState, stateRef]);

  // Determine auth state on boot before connecting any streams. The provider
  // subscribes to ONLY the auth-derived readiness flag; every other state
  // change (sessions, conversations, deltas) leaves the provider un-rendered.
  const authReady = useSyncExternalStore(
    store.subscribe,
    () => {
      const auth = store.getState().auth;
      return auth.checked && (!auth.required || auth.userId !== null);
    },
    () => false
  );

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
  }, [setState]);

  const onLogin = useCallback((userId: string) => {
    aliveRef.current = true;
    lastEventIDRef.current = null;
    setState((p) => ({
      ...p,
      auth: { checked: true, required: true, userId },
      connection: "connecting"
    }));
  }, [setState]);

  const logout = useCallback(async () => {
    await apiLogout();
    handleUnauthorized();
  }, [handleUnauthorized]);

  const setConnection = useCallback(
    (status: ConnectionStatus) => setState((p) => ({ ...p, connection: status })),
    [setState]
  );
  const onStreamRefresh = useCallback(() => {
    void refresh();
  }, [refresh]);

  useEventStream({
    enabled: authReady,
    aliveRef,
    lastEventIdRef: lastEventIDRef,
    onEvent: handleEvent,
    onRefresh: onStreamRefresh,
    onConnectionChange: setConnection
  });

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
    [loadSessionDetails, pollConversation, setState, stateRef]
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
    [setState]
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
  }, [setState]);

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
  }, [setState, stateRef]);

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
    [pollConversation, upsertConvo, setState]
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
    [upsertConvo, stateRef]
  );

  const markNotificationRead = useCallback(async (id: string) => {
    setState((p) => ({
      ...p,
      notifications: p.notifications.map((n) => (n.id === id ? { ...n, read: true } : n))
    }));
    await apiMarkNotificationRead(id);
  }, [setState]);

  // Actions are all stable useCallbacks, so this object is built once and its
  // identity never changes — destructuring an action from useHarness() can
  // never trigger a re-render.
  const actions = useMemo<HarnessActions>(
    () => ({
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

  // Both store and actions are referentially stable, so the context value is
  // stable forever — useContext alone never causes a consumer to re-render.
  const value = useMemo<HarnessContextValue>(() => ({ store, actions }), [store, actions]);

  return <HarnessContext.Provider value={value}>{children}</HarnessContext.Provider>;
}

function useHarnessContext(): HarnessContextValue {
  const ctx = useContext(HarnessContext);
  if (!ctx) throw new Error("useHarness must be used inside HarnessProvider");
  return ctx;
}

/**
 * Subscribe to a derived slice of the store. Backed by useSyncExternalStore
 * with a cached-selector so the snapshot getter returns a STABLE reference
 * whenever the selected value is (by `isEqual`) unchanged — that stability is
 * what both avoids the "getSnapshot should be cached" infinite loop and lets
 * React skip re-renders. Default equality is Object.is (reference equality),
 * which is correct for our reducer since it only replaces a slice object when
 * that slice actually changed.
 */
function useStoreSelector<T>(
  selector: (s: HarnessState) => T,
  isEqual: (a: T, b: T) => boolean = Object.is
): T {
  const { store } = useHarnessContext();
  // Cache of the last value handed out. .current is written/read only inside
  // getSnapshot (call time, not render), so it's rule-compliant. Repeated
  // getSnapshot calls return the same reference until the selection changes.
  const cacheRef = useRef<{ value: T } | null>(null);

  // getSnapshot closes directly over the current-render selector/isEqual (no
  // refs), so a changed selector — e.g. a new sessionId — is reflected with no
  // stale frame. Rebuilding it each render is fine for useSyncExternalStore:
  // only `subscribe` identity governs resubscription, and store is stable.
  const getSnapshot = () => {
    const next = selector(store.getState());
    const cached = cacheRef.current;
    if (cached && isEqual(cached.value, next)) {
      return cached.value;
    }
    cacheRef.current = { value: next };
    return next;
  };

  return useSyncExternalStore(store.subscribe, getSnapshot, getSnapshot);
}

/**
 * useHarness — returns the STABLE actions object. Kept for least-churn
 * compatibility: consumers destructure actions (e.g.
 * `const { sendMessage } = useHarness()`) exactly as before. It carries no
 * live `state`; read state via the slice hooks below so components only
 * re-render on the slices they actually use.
 */
export function useHarness(): HarnessActions {
  return useHarnessContext().actions;
}

export function useSessions() {
  return useStoreSelector((s) => s.sessions);
}

export function useSelectedId() {
  return useStoreSelector((s) => s.selectedId);
}

export function useSelectedSession() {
  return useStoreSelector((s) => (s.selectedId ? s.sessions.find((x) => x.id === s.selectedId) ?? null : null));
}

export function useConversation(sessionId: string | null) {
  return useStoreSelector((s) => (sessionId ? s.conversations[sessionId] ?? EMPTY_CONVO : EMPTY_CONVO));
}

export function useActivity(sessionId: string | null): ConversationActivity {
  // Shallow equality: withActivity rebuilds the activity object on many events
  // (including every delta, which sets phase "streaming"), but when the fields
  // are unchanged this returns the cached reference and skips the re-render.
  return useStoreSelector(
    (s) => (sessionId ? s.conversations[sessionId]?.activity ?? EMPTY_ACTIVITY : EMPTY_ACTIVITY),
    shallowEqual
  );
}

export function useArtifacts(sessionId: string | null) {
  return useStoreSelector((s) => (sessionId ? s.artifacts[sessionId] ?? EMPTY_ARTIFACTS : EMPTY_ARTIFACTS));
}

export function useNotifications() {
  const { actions } = useHarnessContext();
  const notifications = useStoreSelector((s) => s.notifications);
  return { notifications, markNotificationRead: actions.markNotificationRead };
}

// Rarely-changing meta grouped behind a shallow-equal selector so a delta on a
// conversation never re-renders auth/connection/capabilities consumers.
export function useConnection() {
  return useStoreSelector((s) => s.connection);
}

export function useCapabilities() {
  return useStoreSelector((s) => s.capabilities);
}

export function useAuth() {
  return useStoreSelector((s) => s.auth, shallowEqual);
}

export function useReady() {
  return useStoreSelector((s) => s.ready);
}

export function useBootError() {
  return useStoreSelector((s) => s.bootError);
}
