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
  cancelQueuedTurn as apiCancelQueuedTurn,
  createSession as apiCreateSession,
  destroySession as apiDestroySession,
  fetchArtifacts,
  fetchDeploymentCapabilities,
  fetchHealth,
  fetchMe,
  fetchMessagePage,
  fetchMessages,
  fetchNotifications,
  fetchSession,
  fetchSessionRuntime,
  fetchSessions,
  interruptSession as apiInterruptSession,
  logout as apiLogout,
  markNotificationRead as apiMarkNotificationRead,
  postMessage as apiPostMessage,
  postMessageFeedback,
  renameSession as apiRenameSession,
  sendQueuedTurn as apiSendQueuedTurn
} from "@/lib/api";
import { clearClientResources, registerCache } from "@/lib/cache-lifecycle";
import { invalidateArtifacts, clearArtifactInvalidations } from "@/lib/artifact-invalidation";
import { boundedMessage, clearReadingAnchors, mergeMessageWindow, readingAnchor } from "@/lib/message-window";
import { markWorkbench } from "@/lib/performance";
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
import { isFinalReply } from "@/lib/message-grouping";
import { isTerminal } from "@/lib/format";
import { reconcileSessionList, mergeSessionList, reduceSessionEvent, type SessionEventNotification } from "@/lib/session-events";
import { translate } from "@/lib/i18n";
import { detectLocale } from "@/lib/i18n/locale";
import { reduceRuntimeActivity, shouldApplyRuntime } from "@/lib/session-runtime";
import { useEventStream } from "@/lib/use-event-stream";
import { buildEventsStreamUrl } from "@/lib/ws";
import type {
  ApiArtifact,
  ApiNotification,
  DeploymentCapabilities,
  ApiMessage,
  MessagePage,
  ApiSession,
  ApiSessionRuntime,
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
  /**
   * Whether this account may upload files into a workspace, from /api/me.
   * Deny-by-default: it stays false until the backend says otherwise, so a
   * failed or stale /api/me withholds the upload UI rather than offering
   * buttons whose requests the server would reject with 403.
   */
  canUpload: boolean;
};

type HarnessState = {
  ready: boolean;
  bootError: string | null;
  connection: ConnectionStatus;
  capabilities: DeploymentCapabilities | null;
  sessions: ApiSession[];
  selectedId: string | null;
  conversations: Record<string, ConversationState>;
  runtimes: Record<string, ApiSessionRuntime | null>;
  artifacts: Record<string, ApiArtifact[]>;
  notifications: ApiNotification[];
  auth: AuthState;
};

// The stable action surface. Identity never changes across the provider's
// lifetime, so destructuring these from useHarness() never causes a re-render.
type HarnessActions = {
  selectSession: (id: string | null) => void;
  createSession: (mode: SessionMode) => Promise<{ ok: boolean; error?: string; session?: ApiSession }>;
  /** Remove a client-created session that was abandoned before its first message. */
  discardEmptySession: (id?: string) => Promise<{ ok: boolean; error?: string }>;
  destroySession: (id: string) => Promise<{ ok: boolean; error?: string }>;
  renameSession: (id: string, title: string) => Promise<{ ok: boolean; error?: string }>;
  interruptSession: (id: string) => Promise<{ ok: boolean; error?: string }>;
  /** Withdraw a turn the user queued but no longer wants. */
  cancelQueuedTurn: (id: string, turnId: number) => Promise<{ ok: boolean; error?: string }>;
  /**
   * Send one queued turn now: steered into the running turn if there is one,
   * otherwise released to run on its own. The server decides which.
   */
  sendQueuedTurn: (id: string, turnId: number) => Promise<{ ok: boolean; error?: string }>;
  sendMessage: (id: string, content: string) => Promise<{ ok: boolean; error?: string }>;
  submitFeedback: (
    sessionId: string,
    messageId: number,
    rating: "up" | "down" | null
  ) => Promise<{ ok: boolean; error?: string }>;
  refresh: () => Promise<void>;
  loadOlderMessages: (id: string) => Promise<void>;
  loadMessageWindow: (id: string, aroundId?: number) => Promise<void>;
  loadNewerMessages: (id: string) => Promise<void>;
  onLogin: (userId: string, canUpload: boolean) => void;
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
  runtimes: {},
  artifacts: {},
  notifications: [],
  auth: { checked: false, required: false, userId: null, canUpload: false }
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

/**
 * Wording for a generation.error toast. The server's `error` field is a
 * diagnostic string (an exit status and a stderr line, or a recovery class such
 * as sandbox_heartbeat_lost); for the faults a user can do something about, say
 * what happened and what it means for them instead. Unknown classes fall back
 * to the server text, which is still better than nothing.
 */
function runtimeFaultMessage(notification: SessionEventNotification): string {
  const locale = detectLocale();
  switch (notification.errorClass) {
    case "sandbox_exited":
    case "sandbox_heartbeat_lost":
    case "orchestrator_restart_reconnect_grace_expired":
    case "runtime_reconciled_stall":
      return translate(locale, "runtime.fault.sandboxLost");
    case "sandbox_oom":
      return translate(locale, "runtime.fault.sandboxOOM");
    default:
      return notification.message;
  }
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
  const messageRequestsRef = useRef<Map<string, AbortController>>(new Map());
  const authEpochRef = useRef(0);
  const hotSessionsRef = useRef<string[]>([]);
  const lastEventIDRef = useRef<number | null>(null);
  // A session is created before the composer can send its first message. Keep
  // that short-lived state client-side so abandoning it does not leave a
  // meaningless `sess_*` row in History. Once a message is accepted, the id is
  // removed and the session becomes normal durable history.
  const draftSessionIdsRef = useRef<Set<string>>(new Set());
  // Prevent an abandon action from racing a message request that has not yet
  // returned. The conversation is still empty locally while that request is
  // in flight, but deleting it would lose a message the user did send.
  const pendingMessageSessionIdsRef = useRef<Set<string>>(new Set());
  // The create event can arrive over SSE slightly after the local cleanup. Keep
  // a tombstone so a late `session.created` event cannot resurrect the row.
  const discardedSessionIdsRef = useRef<Set<string>>(new Set());

  const ensureConvo = useCallback((convos: HarnessState["conversations"], id: string) => {
    if (convos[id] || (stateRef.current.selectedId !== id && !hotSessionsRef.current.includes(id))) return convos;
    return { ...convos, [id]: emptyConvo() };
  }, [stateRef]);

  const upsertConvo = useCallback(
    (id: string, patch: (prev: ConversationState) => ConversationState) => {
      setState((prev) => {
        if (prev.selectedId !== id && !hotSessionsRef.current.includes(id)) return prev;
        const current = prev.conversations[id] ?? emptyConvo();
        const conversations = { ...prev.conversations, [id]: patch(current) };
        let bytes = Object.values(conversations).reduce((total,c) => total + c.messages.reduce((n,m) => n + m.content.length * 2 + 1024, 0), 0);
        for (const key of [...hotSessionsRef.current].reverse()) {
          if (bytes <= 32 * 1024 * 1024) break;
          if (key === prev.selectedId || !conversations[key]) continue;
          bytes -= conversations[key].messages.reduce((n,m) => n + m.content.length * 2 + 1024, 0);
          delete conversations[key];
        }
        return { ...prev, conversations };
      });
    },
    [setState]
  );

  /**
   * Apply a runtime snapshot, however it arrived (GET, POST response, or the
   * `session.runtime` event). This is the ONLY writer of runtime state, and the
   * single place activity is reconciled with the server's view.
   *
   * Two rules make the whole thing convergent:
   *
   * 1. Revision guard. A snapshot older than the one held is dropped. Requests
   *    and pushes race constantly, and without this a slow GET can land after a
   *    newer push and restore a queue entry that was already drained — the row
   *    then never clears, because nothing contradicts it again.
   *
   * 2. Activity is subordinate to the snapshot. No active turn means idle, full
   *    stop. Live events refine the phase of a turn the snapshot says is running;
   *    they never invent one. This is what fixes an idle session showing
   *    "Thinking", and it holds by construction rather than by patching each
   *    event that could leave a stale phase behind.
   */
  const applyRuntime = useCallback(
    (id: string, snapshot: ApiSessionRuntime) => {
      setState((prev) => {
        if (!shouldApplyRuntime(prev.runtimes[id], snapshot)) return prev;
        const convo = prev.conversations[id] ?? emptyConvo();
        return {
          ...prev,
          runtimes: prev.selectedId === id || hotSessionsRef.current.includes(id) ? { ...prev.runtimes, [id]: snapshot } : prev.runtimes,
          conversations: prev.selectedId === id || hotSessionsRef.current.includes(id) ? { ...prev.conversations, [id]: reduceRuntimeActivity(convo, snapshot) } : prev.conversations,
          // The snapshot carries the session status from the same transaction that
          // changed the turns, so it is never staler than the session list's copy.
          sessions: prev.sessions.map((session) =>
            session.id === id
              ? { ...session, status: snapshot.session_status, sandbox: snapshot.sandbox ?? session.sandbox }
              : session
          )
        };
      });
    },
    [setState]
  );

  /**
   * Re-read the projection from the server. Used on the edges where turn state
   * changed but the response did not carry a snapshot; the revision guard makes a
   * late arrival harmless.
   */
  const refreshRuntime = useCallback(
    (id: string) => {
      const epoch = authEpochRef.current;
      void fetchSessionRuntime(id).then((res) => {
        if (epoch === authEpochRef.current && res.ok) applyRuntime(id, res.data);
      });
    },
    [applyRuntime]
  );

  const discardEmptySession = useCallback(
    async (requestedId?: string) => {
      const id = requestedId ?? stateRef.current.selectedId;
      if (!id || !draftSessionIdsRef.current.has(id)) return { ok: true as const };

      // Never discard a session while its first message is being submitted, or
      // after an event has already populated the local conversation.
      if (pendingMessageSessionIdsRef.current.has(id)) return { ok: true as const };
      const session = stateRef.current.sessions.find((item) => item.id === id);
      if (!session || session.access === "reader") {
        draftSessionIdsRef.current.delete(id);
        return { ok: true as const };
      }
      // A manual title or uploaded file is also meaningful work. Keep the
      // session in History even if the user has not sent a chat message yet.
      if (session.title?.trim() || (stateRef.current.artifacts[id] ?? []).length > 0) {
        draftSessionIdsRef.current.delete(id);
        return { ok: true as const };
      }
      const conversation = stateRef.current.conversations[id];
      if (conversation && conversation.messages.length > 0) {
        draftSessionIdsRef.current.delete(id);
        return { ok: true as const };
      }

      // Hide it immediately; the server delete below is the durable cleanup.
      // If that request fails, restore the row and allow a later navigation to
      // retry rather than silently losing a session from the local view.
      draftSessionIdsRef.current.delete(id);
      discardedSessionIdsRef.current.add(id);
      setState((p) => {
        const conversations = { ...p.conversations };
        delete conversations[id];
        const artifacts = { ...p.artifacts };
        delete artifacts[id];
        return {
          ...p,
          sessions: p.sessions.filter((item) => item.id !== id),
          conversations,
          artifacts,
          runtimes: { ...p.runtimes, [id]: null },
          selectedId: p.selectedId === id ? null : p.selectedId
        };
      });

      const res = await apiDestroySession(id);
      // A retention sweep may have already removed the abandoned session. In
      // that case the desired end state is already true, so treat 404 as a
      // successful discard instead of restoring a ghost row locally.
      if (!res.ok && res.status !== 404) {
        discardedSessionIdsRef.current.delete(id);
        draftSessionIdsRef.current.add(id);
        setState((p) => ({
          ...p,
          sessions: p.sessions.some((item) => item.id === id) ? p.sessions : [session, ...p.sessions],
          conversations: p.conversations[id]
            ? p.conversations
            : { ...p.conversations, [id]: conversation ?? emptyConvo() },
          artifacts: p.artifacts[id] ? p.artifacts : { ...p.artifacts, [id]: [] }
        }));
        return { ok: false as const, error: res.error };
      }
      return { ok: true as const };
    },
    [setState, stateRef]
  );

  const readMessageWindow = useCallback(async (id: string, options: { beforeId?: number; afterId?: number; aroundId?: number; replace?: boolean } = {}) => {
    const key = `${id}:messages`;
    if (messageRequestsRef.current.has(key)) return;
    const controller = new AbortController();
    const epoch = authEpochRef.current;
    messageRequestsRef.current.set(key, controller);
    const baseline = stateRef.current.conversations[id]?.messages ?? [];
    const paged = stateRef.current.capabilities?.message_pages && process.env.NEXT_PUBLIC_HARNESS_MESSAGE_PAGES !== "0";
    upsertConvo(id, (c) => ({ ...c, loading: !c.historyLoaded, loadingOlder: !!options.beforeId, error: null }));
    const res = paged ? await fetchMessagePage(id, { ...options, signal: controller.signal })
      : await fetchMessages(id, controller.signal);
    if (controller.signal.aborted || authEpochRef.current !== epoch) return;
    messageRequestsRef.current.delete(key);
    upsertConvo(id, (c) => {
      if (!res.ok) return { ...c, ...(res.status === 403 || res.status === 404 ? { messages: [], streaming: [], liveTrace: [], historyLoaded: false } : {}), loading: false, loadingOlder: false, error: res.error };
      const page = "has_older" in res.data ? res.data as MessagePage : null;
      const incoming = res.data.messages ?? [];
      const center = options.beforeId ? incoming[0]?.id : options.afterId ? incoming.at(-1)?.id : options.aroundId;
      const messages = paged ? mergeMessageWindow(options.replace ? c.messages.filter((m) => !baseline.includes(m) || m.id > (page?.latest_id ?? 0)) : c.messages, incoming, baseline, center) : incoming;
      const latest = page?.latest_id ?? messages.at(-1)?.id ?? 0;
      const tailWatermark = page ? (options.aroundId || options.beforeId ? c.tailWatermark : page.has_newer ? page.after_id : page.latest_id) : latest;
      return { ...c, messages, loading: false, loadingOlder: false, historyLoaded: true,
        hasOlder: page ? !incoming.length || (messages[0]?.id ?? 0) < page.before_id ? c.hasOlder : page.has_older || (messages[0]?.id ?? 0) > page.before_id : false,
        hasNewer: page ? page.has_newer || (messages.at(-1)?.id ?? 0) < latest : false,
        tailWatermark, latestID: Math.max(c.latestID, latest), latestFinalID: page?.latest_final_id ?? 0,
        interruption: page?.interruption ?? "" };
    });
    markWorkbench("message-merge");
  }, [stateRef, upsertConvo]);

  const loadMessageWindow = useCallback(async (id: string, aroundId?: number) => {
    messageRequestsRef.current.get(`${id}:messages`)?.abort();
    messageRequestsRef.current.delete(`${id}:messages`);
    await readMessageWindow(id, { aroundId, replace: true });
  }, [readMessageWindow]);
  const loadOlderMessages = useCallback(async (id: string) => {
    const c = stateRef.current.conversations[id];
    if (c?.hasOlder && !c.loadingOlder) await readMessageWindow(id, { beforeId: c.messages[0]?.id });
  }, [readMessageWindow, stateRef]);
  const loadNewerMessages = useCallback(async (id: string) => {
    const c = stateRef.current.conversations[id];
    if (c?.hasNewer) await readMessageWindow(id, { afterId: c.messages.at(-1)?.id });
  }, [readMessageWindow, stateRef]);
  const loadSessionDetails = useCallback((id: string, skipLoaded = false) => {
    refreshRuntime(id);
    const c = stateRef.current.conversations[id];
    if (skipLoaded && c?.historyLoaded) return;
    const anchor = readingAnchor(id);
    void readMessageWindow(id, { aroundId: anchor && !anchor.following ? anchor.messageId : undefined, replace: true });
    const epoch = authEpochRef.current;
    if (stateRef.current.capabilities?.artifact_pages && process.env.NEXT_PUBLIC_HARNESS_ARTIFACT_PAGES !== "0") return;
    void fetchArtifacts(id).then((res) => {
      if (res.ok && epoch === authEpochRef.current) setState((p) => ({ ...p, artifacts: { ...p.artifacts, [id]: res.data.artifacts ?? [] } }));
    });
  }, [refreshRuntime, stateRef, readMessageWindow, setState]);

  const pollConversation = useCallback(async (id: string) => {
    const streamLive = () => stateRef.current.connection === "live";
    if (pollingRef.current.has(id) || streamLive()) return;
    pollingRef.current.add(id);
    const epoch = authEpochRef.current;
    try {
      const deadline = Date.now() + MESSAGE_POLL_TIMEOUT_MS;
      while (aliveRef.current && epoch === authEpochRef.current && stateRef.current.selectedId === id && !streamLive() && Date.now() < deadline) {
        const c = stateRef.current.conversations[id];
        await readMessageWindow(id, c?.hasNewer ? { aroundId: readingAnchor(id)?.messageId, replace: true } : { afterId: c?.tailWatermark || undefined });
        refreshRuntime(id);
        await delay(MESSAGE_POLL_INTERVAL_MS);
      }
    } finally { pollingRef.current.delete(id); }
  }, [readMessageWindow, refreshRuntime, stateRef]);

  const refreshNotifications = useCallback(async () => {
    const epoch = authEpochRef.current;
    const res = await fetchNotifications();
    if (epoch !== authEpochRef.current) return;
    if (!res.ok) return;
    setState((p) => ({ ...p, notifications: res.data.notifications ?? [] }));
  }, [setState]);

  // Re-fetch only the session list, leaving selection and loaded conversations
  // alone. Deliberately narrower than refresh(): callers below run this in the
  // background while the user is reading something.
  const refreshSessions = useCallback(async () => {
    const epoch = authEpochRef.current;
    const before = stateRef.current.sessions;
    const res = await fetchSessions();
    if (epoch !== authEpochRef.current) return;
    // Bail silently on failure: a transient error must not empty a healthy list.
    if (!res.ok) return;
    const list = (res.data.sessions ?? []).filter(
      (session) => !discardedSessionIdsRef.current.has(session.id)
    );
    setState((p) => {
      return { ...p, sessions: reconcileSessionList(before, p.sessions, list) };
    });
  }, [setState, stateRef]);

  const handleEvent = useCallback(
    (event: HarnessEvent) => {
      const sessionId = event.session_id;
      const time = event.time ?? new Date().toISOString();
      /**
       * Apply a session-level event, then reconcile the runtime projection.
       *
       * `reconcileRuntime` says whether this event needs a follow-up GET. Events
       * that ride alongside a `session.runtime` push (the normal turn lifecycle)
       * do not: the push carries the same snapshot, so a GET here would only add
       * a request that the revision guard then discards.
       *
       * Failure and recovery paths DO need it. Reconciliation, lease expiry and
       * generation teardown change turn state through the store without routing a
       * snapshot out (see ReconcileStaleSessionStates), so the push that would
       * have corrected the client never comes — and without a re-read the queue
       * keeps rendering turns that are no longer going to run.
       */
      const applySessionEvent = (reconcileRuntime = false) => {
        const preview = reduceSessionEvent(
          { sessions: stateRef.current.sessions, conversations: stateRef.current.conversations },
          event,
          { emptyConversation: emptyConvo, time }
        );
        if (!preview.handled) return false;
        for (const notification of preview.notifications) {
          if (notification.level === "error") {
            toast.error(runtimeFaultMessage(notification), { duration: 6000 });
          }
        }
        setState((p) => {
          const next = reduceSessionEvent(
            { sessions: p.sessions, conversations: p.conversations },
            event,
            { emptyConversation: emptyConvo, time }
          );
          return { ...p, sessions: next.sessions, conversations: Object.fromEntries(Object.entries(next.conversations).filter(([id]) => id === p.selectedId || hotSessionsRef.current.includes(id))) };
        });
        // Whether the agent is still busy is decided by the turn queue, so the
        // projection settles activity rather than this event guessing from the
        // session status. The revision guard makes a late arrival harmless.
        if (sessionId && reconcileRuntime) refreshRuntime(sessionId);
        return true;
      };
      switch (event.type) {
        case "session.created": {
          const sess = readSession(event.payload);
          if (!sess) return;
          if (discardedSessionIdsRef.current.has(sess.id)) return;
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
          // Some status transitions carry no projection with them: cold fallback
          // and automatic turn resume both change turn state and publish only
          // this status event (see turn_resume.go, coldFallback). Re-read so the
          // queue reflects the requeued/new turn.
          applySessionEvent(true);
          return;
        }
        // The runtime projection, pushed whenever turn state changed. Same payload
        // as the endpoint, and applyRuntime's revision guard means it can arrive in
        // any order relative to a request the client already had in flight.
        case "session.runtime": {
          if (!sessionId || !isRecord(event.payload)) return;
          const snapshot = event.payload as unknown as ApiSessionRuntime;
          if (typeof snapshot.revision !== "number") return;
          applyRuntime(sessionId, snapshot);
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
          if (!hotSessionsRef.current.includes(sessionId) && stateRef.current.selectedId !== sessionId) return;
          const raw = event.payload as ApiMessage | undefined;
          const paged = stateRef.current.capabilities?.message_pages && process.env.NEXT_PUBLIC_HARNESS_MESSAGE_PAGES !== "0";
          const msg = raw && typeof raw.content === "string" && paged ? boundedMessage(raw) : raw;
          if (!msg || typeof msg.id !== "number" || typeof msg.content !== "string") return;
          if (msg.role === "user") draftSessionIdsRef.current.delete(sessionId);
          // Every message the server broadcasts belongs in the transcript: a turn
          // still queued has no message row exposed (listMessages excludes it), so
          // there is nothing here to filter and no way to double-render one.
          upsertConvo(sessionId, (c) => ({
            ...c,
            // A new user turn begins here: drop any live trace left from before.
            liveTrace: msg.role === "user" ? [] : c.liveTrace,
            interruption: msg.role === "user" ? "" : c.interruption,
            messages: !paged ? c.messages.some((m) => m.id === msg.id) ? c.messages : [...c.messages, msg] : c.hasNewer ? c.messages : mergeMessageWindow(c.messages, [msg], c.messages, readingAnchor(sessionId)?.following === false ? readingAnchor(sessionId)?.messageId : undefined),
            latestID: Math.max(c.latestID, msg.id), hasNewer: c.hasNewer
          }));
          return;
        }
        case "agent.message": {
          if (!sessionId) return;
          if (!hotSessionsRef.current.includes(sessionId) && stateRef.current.selectedId !== sessionId) return;
          const raw = event.payload as ApiMessage | undefined;
          const paged = stateRef.current.capabilities?.message_pages && process.env.NEXT_PUBLIC_HARNESS_MESSAGE_PAGES !== "0";
          const msg = raw && typeof raw.content === "string" && paged ? boundedMessage(raw) : raw;
          if (!msg || typeof msg.id !== "number" || typeof msg.content !== "string") return;
          upsertConvo(sessionId, (c) => ({
            ...c,
            streaming: Array.isArray((event.payload as { stream_ids?: string[] }).stream_ids) ? c.streaming.filter((stream) => !(event.payload as { stream_ids: string[] }).stream_ids.includes(stream.id)) : pruneCommittedStreaming(c.streaming, [msg]),
            // The committed assistant message renders its own server trace; clear
            // the live nodes so they don't duplicate below it.
            liveTrace: [],
            latestFinalID: msg.stop_reason === "stop" ? msg.id : c.latestFinalID,
            interruption: msg.stop_reason === "stop" ? "" : msg.stop_reason === "sandbox_died" ? "sandbox" : msg.stop_reason === "error" ? "model" : c.interruption,
            messages: !paged ? c.messages.some((m) => m.id === msg.id) ? c.messages : [...c.messages, msg] : c.hasNewer ? c.messages : mergeMessageWindow(c.messages, [msg], c.messages, readingAnchor(sessionId)?.following === false ? readingAnchor(sessionId)?.messageId : undefined),
            latestID: Math.max(c.latestID, msg.id), hasNewer: c.hasNewer
          }));
          return;
        }
        case "agent.delta": {
          if (!sessionId || !isRecord(event.payload)) return;
          if (stateRef.current.selectedId !== sessionId) return;
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
          // A failure retires the generation and requeues/fails turns in the
          // store directly, without routing a snapshot out — re-read it.
          applySessionEvent(true);
          return;
        }
        case "generation.error": {
          // A failure retires the generation and requeues/fails turns in the
          // store directly, without routing a snapshot out — re-read it.
          applySessionEvent(true);
          return;
        }
        case "session.checkpoint_retired":
        case "session.restore_fallback_retired": {
          // Retirement paths change turn state without publishing a projection.
          applySessionEvent(true);
          return;
        }
        case "ack_turn_completed": {
          // handleBridgeCompletion already pushes the post-completion projection.
          applySessionEvent();
          return;
        }
        case "artifact.updated": {
          if (sessionId && stateRef.current.capabilities?.artifact_pages && process.env.NEXT_PUBLIC_HARNESS_ARTIFACT_PAGES !== "0") { invalidateArtifacts(sessionId); return; }
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
          if (sessionId && stateRef.current.capabilities?.artifact_pages && process.env.NEXT_PUBLIC_HARNESS_ARTIFACT_PAGES !== "0") { invalidateArtifacts(sessionId); return; }
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
    [
      ensureConvo,
      upsertConvo,
      refreshNotifications,
      refreshSessions,
      refreshRuntime,
      applyRuntime,
      setState,
      stateRef
    ]
  );


  const handleUnauthorized = useCallback(() => {
    authEpochRef.current++;
    clearClientResources();
    for (const request of messageRequestsRef.current.values()) request.abort();
    messageRequestsRef.current.clear();
    hotSessionsRef.current = [];
    clearReadingAnchors();
    clearArtifactInvalidations();
    aliveRef.current = false;
    draftSessionIdsRef.current.clear();
    pendingMessageSessionIdsRef.current.clear();
    discardedSessionIdsRef.current.clear();
    setState((p) => ({
      ...p,
      auth: { checked: true, required: true, userId: null, canUpload: false },
      sessions: [],
      conversations: {},
      artifacts: {},
      runtimes: {},
      selectedId: null,
      connection: "down"
    }));
  }, [setState]);

  const refresh = useCallback(async () => {
    const epoch = authEpochRef.current;
    const before = stateRef.current.sessions;
    const [health, sessions, capabilities] = await Promise.all([fetchHealth(), fetchSessions(), fetchDeploymentCapabilities()]);
    if (epoch !== authEpochRef.current) return;
    if (!health.ok) {
      setState((p) => ({ ...p, ready: true, bootError: health.error }));
      return;
    }
    if (!sessions.ok) {
      if (sessions.status === 401) {
        handleUnauthorized();
        return;
      }
      setState((p) => ({ ...p, ready: true, bootError: sessions.error }));
      return;
    }

    const list = (sessions.data.sessions ?? []).filter(
      (session) => !discardedSessionIdsRef.current.has(session.id)
    );
    const currentSelected = stateRef.current.selectedId;
    const selectable = list.filter((session) => !isTerminal(session.status));
    const selectedId =
      currentSelected && list.some((session) => session.id === currentSelected)
        ? currentSelected
        : selectable[0]?.id ?? null;
    if (selectedId) hotSessionsRef.current = [selectedId, ...hotSessionsRef.current.filter((id) => id !== selectedId)].slice(0, 5);
    setState((p) => {
      const conversations = { ...p.conversations };
      if (selectedId && !conversations[selectedId]) conversations[selectedId] = emptyConvo();
      return {
        ...p,
        ready: true,
        bootError: capabilities.ok ? null : capabilities.error,
        capabilities: capabilities.ok ? capabilities.data : p.capabilities,
        sessions: reconcileSessionList(before, p.sessions, list),
        selectedId,
        runtimes: Object.fromEntries(Object.keys(conversations).map((id) => [id, p.runtimes[id] ?? null])),
        conversations
      };
    });
    if (selectedId) {
      const selected = list.find((s) => s.id === selectedId);
      // Reconnect/replay-gap recovery must fetch cached details too: messages and
      // artifacts may have changed while this tab was disconnected.
      loadSessionDetails(selectedId);
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
        setState((p) => ({
          ...p,
          auth: { checked: true, required: false, userId: null, canUpload: false }
        }));
        return;
      }
      setState((p) => ({
        ...p,
        auth: {
          checked: true,
          required: res.data.auth_required,
          userId: res.data.user_id || null,
          canUpload: res.data.can_upload === true
        }
      }));
    });
    return () => {
      cancelled = true;
    };
  }, [setState]);

  const onLogin = useCallback((userId: string, canUpload: boolean) => {
    aliveRef.current = true;
    lastEventIDRef.current = null;
    setState((p) => ({
      ...p,
      auth: { checked: true, required: true, userId, canUpload },
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
  const refreshingRef = useRef(false);
  const refreshAgainRef = useRef(false);
  const onStreamRefresh = useCallback(() => {
    if (refreshingRef.current) { refreshAgainRef.current = true; return; }
    refreshingRef.current = true;
    setState((p) => ({ ...p, conversations: Object.fromEntries(Object.entries(p.conversations).map(([id,c]) => [id, { ...c, historyLoaded: false }])) }));
    void refresh().finally(() => {
      refreshingRef.current = false;
      if (refreshAgainRef.current) { refreshAgainRef.current = false; void refresh(); }
    });
  }, [refresh, setState]);
  const pendingDeltas = useRef(new Map<string, HarnessEvent>());
  const deltaTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const flushDeltas = useCallback(() => {
    if (deltaTimer.current) clearTimeout(deltaTimer.current);
    deltaTimer.current = null;
    const events = [...pendingDeltas.current.values()]; pendingDeltas.current.clear();
    for (const event of events) handleEvent(event);
  }, [handleEvent]);
  const receiveEvent = useCallback((event: HarnessEvent) => {
    if (event.type !== "agent.delta") {
      if (event.type === "agent.message" || event.type === "message.created" || event.type === "ack_turn_completed" || event.type === "generation.error" || event.type === "session.error" || event.type === "session.runtime") flushDeltas();
      handleEvent(event); return;
    }
    if (event.session_id !== stateRef.current.selectedId || !isRecord(event.payload)) return;
    const delta = readAgentDelta(event.payload); if (!delta) return;
    const key = `${event.session_id}:${delta.id}`;
    const previous = pendingDeltas.current.get(key);
    const old = previous && isRecord(previous.payload) ? readAgentDelta(previous.payload)?.text ?? "" : "";
    pendingDeltas.current.set(key, { ...event, payload: { ...event.payload, text: (old + delta.text).slice(0, 8192) } });
    if (!deltaTimer.current) deltaTimer.current = setTimeout(flushDeltas, 50);
  }, [flushDeltas, handleEvent, stateRef]);
  useEffect(() => {
    const pending = pendingDeltas.current;
    const clear = () => { if (deltaTimer.current) clearTimeout(deltaTimer.current); deltaTimer.current = null; pending.clear(); };
    const unregister = registerCache(clear);
    return () => { clear(); unregister(); };
  }, []);

  useEventStream({
    enabled: authReady,
    aliveRef,
    lastEventIdRef: lastEventIDRef,
    onEvent: receiveEvent,
    onRefresh: onStreamRefresh,
    onConnectionChange: setConnection
  });

  // Reconcile even idle sessions: their sandbox can be released without a turn
  // event, and a quiet/broken stream must not freeze the UI indefinitely.
  useEffect(() => {
    if (!authReady) return;
    let inFlight = false;
    const tick = () => {
      if (inFlight || document.visibilityState === "hidden") return;
      inFlight = true;
      const selectedId = stateRef.current.selectedId;
      const current = selectedId ? stateRef.current.conversations[selectedId] : undefined;
      if (selectedId && current?.historyLoaded) void readMessageWindow(selectedId, current.hasNewer ? { aroundId: readingAnchor(selectedId)?.messageId ?? current.messages[0]?.id } : { afterId: current.tailWatermark || undefined });
      void Promise.all([
        refreshSessions(),
        selectedId
          ? fetchSessionRuntime(selectedId).then((res) => {
              if (res.ok) applyRuntime(selectedId, res.data);
            })
          : Promise.resolve()
      ]).finally(() => { inFlight = false; });
    };
    const timer = window.setInterval(tick, 15_000);
    document.addEventListener("visibilitychange", tick);
    window.addEventListener("online", tick);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", tick);
      window.removeEventListener("online", tick);
    };
  }, [authReady, refreshSessions, stateRef, applyRuntime, readMessageWindow]);

  const selectSession = useCallback(
    (id: string | null) => {
      markWorkbench("select");
      const previousId = stateRef.current.selectedId;
      if (previousId !== id) {
        if (previousId) upsertConvo(previousId, (c) => ({ ...c, loading: false, loadingOlder: false }));
        for (const [key, request] of messageRequestsRef.current) { request.abort(); messageRequestsRef.current.delete(key); }
      }
      if (id) {
        hotSessionsRef.current = [id, ...hotSessionsRef.current.filter((x) => x !== id)].slice(0, 5);
        setState((p) => ({ ...p, conversations: Object.fromEntries(Object.entries(p.conversations).filter(([key]) => hotSessionsRef.current.includes(key))), runtimes: Object.fromEntries(Object.entries(p.runtimes).filter(([key]) => hotSessionsRef.current.includes(key))), artifacts: Object.fromEntries(Object.entries(p.artifacts).filter(([key]) => hotSessionsRef.current.includes(key))) }));
      }
      if (previousId && previousId !== id) void discardEmptySession(previousId);
      setState((p) => ({ ...p, selectedId: id }));
      if (!id) return;
      const session = stateRef.current.sessions.find((s) => s.id === id);
      // Same reasoning as refresh(): a reader has no live stream, so re-opening a
      // shared session must refetch rather than trust a cached first load.
      loadSessionDetails(id, session?.access !== "reader");
      if (session && isActiveStatus(session.status)) {
        void pollConversation(id);
      }
    },
    [discardEmptySession, loadSessionDetails, pollConversation, setState, stateRef, upsertConvo]
  );

  const createSession = useCallback(
    async (mode: SessionMode) => {
      // Starting another conversation abandons the current untouched draft.
      // A session with a message is left alone and remains in History.
      await discardEmptySession();
      const res = await apiCreateSession(mode);
      if (!res.ok) return { ok: false as const, error: res.error };
      const session = res.data;
      draftSessionIdsRef.current.add(session.id);
      discardedSessionIdsRef.current.delete(session.id);
      setState((p) => ({
        ...p,
        sessions: [session, ...p.sessions.filter((s) => s.id !== session.id)],
        selectedId: session.id,
        conversations: { ...p.conversations, [session.id]: emptyConvo() },
        runtimes: { ...p.runtimes, [session.id]: null }
      }));
      return { ok: true as const, session };
    },
    [discardEmptySession, setState]
  );

  const destroySession = useCallback(async (id: string) => {
    draftSessionIdsRef.current.delete(id);
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
    draftSessionIdsRef.current.delete(id);
    setState((p) => ({
      ...p,
      sessions: p.sessions.map((s) => (s.id === session.id ? { ...s, ...session } : s))
    }));
    return { ok: true as const };
  }, [setState, stateRef]);

  const interruptSession = useCallback(async (id: string) => {
    const res = await apiInterruptSession(id);
    if (!res.ok) return { ok: false as const, error: res.error };
    // The response carries the post-interrupt projection, so the composer leaves
    // the busy state on this tick rather than after a round trip through events.
    if (res.data.runtime) applyRuntime(id, res.data.runtime);
    // Stop ends the current turn only; follow-ups the user queued behind it stay
    // put and wait for the user to send them. Say so, because an untouched queue
    // sitting there could otherwise read either as dropped or as about to run.
    const pending = res.data.pending_queued ?? 0;
    if (pending > 0) {
      const locale = detectLocale();
      toast.info(
        translate(locale, pending === 1 ? "runtime.stopped.queueKeptOne" : "runtime.stopped.queueKeptMany", {
          count: pending
        })
      );
    }
    return { ok: true as const };
  }, [applyRuntime]);

  const cancelQueuedTurn = useCallback(async (id: string, turnId: number) => {
    const res = await apiCancelQueuedTurn(id, turnId);
    if (!res.ok) {
      // 409 means the worker claimed it while the click was in flight: it is
      // running now, so re-read and let the user Stop it instead.
      if (res.status === 409) refreshRuntime(id);
      return { ok: false as const, error: res.error };
    }
    applyRuntime(id, res.data);
    return { ok: true as const };
  }, [applyRuntime, refreshRuntime]);

  const sendQueuedTurn = useCallback(async (id: string, turnId: number) => {
    const res = await apiSendQueuedTurn(id, turnId);
    if (!res.ok) {
      // 409 means the row moved on between render and click — claimed, cancelled,
      // or already sent. Re-read rather than retry: the queue the button was drawn
      // from is stale, and a retry would act on the same stale row.
      if (res.status === 409) refreshRuntime(id);
      return { ok: false as const, error: res.error };
    }
    if (res.data.runtime) applyRuntime(id, res.data.runtime);
    // A steer puts the prompt in the transcript straight away — the agent has been
    // told, so the user has to see what was said. The `message.created` event
    // carries it, and this poll covers the case where the stream is not attached.
    if (res.data.status === "steered") void pollConversation(id);
    return { ok: true as const };
  }, [applyRuntime, pollConversation, refreshRuntime]);

  const sendMessage = useCallback(
    async (id: string, content: string) => {
      pendingMessageSessionIdsRef.current.add(id);
      try {
        const res = await apiPostMessage(id, content);
        if (!res.ok) return { ok: false as const, error: res.error };
        draftSessionIdsRef.current.delete(id);
        // The response carries no message, by design: the prompt is queued, not
        // asked. It enters the transcript via `message.created` when the agent
        // actually starts the turn, so its timestamp is the dispatch time and the
        // queue is its sole owner until then. Nothing optimistic is inserted here,
        // which is why the transcript and the queue can never show it at once.
        applyRuntime(id, res.data.runtime);
        void pollConversation(id);
        return { ok: true as const };
      } finally {
        pendingMessageSessionIdsRef.current.delete(id);
      }
    },
    [pollConversation, applyRuntime]
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
      discardEmptySession,
      destroySession,
      renameSession,
      interruptSession,
      cancelQueuedTurn,
      sendQueuedTurn,
      sendMessage,
      submitFeedback,
      refresh,
      loadOlderMessages,
      loadMessageWindow,
      loadNewerMessages,
      onLogin,
      logout,
      markNotificationRead
    }),
    [
      selectSession,
      createSession,
      discardEmptySession,
      destroySession,
      renameSession,
      interruptSession,
      cancelQueuedTurn,
      sendQueuedTurn,
      sendMessage,
      submitFeedback,
      refresh,
      loadOlderMessages,
      loadMessageWindow,
      loadNewerMessages,
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

export function useSessionRuntime(sessionId: string | null): ApiSessionRuntime | null {
  return useStoreSelector((s) => (sessionId ? s.runtimes[sessionId] ?? null : null));
}

/**
 * The last committed assistant reply that ends a turn, or null.
 *
 * A narrow selector on purpose: it lets the workspace panel derive "files this
 * reply mentions" without subscribing to the whole conversation, which would
 * re-render the file tree on every streamed token. Committed messages are never
 * mutated in place, so the selected reference is stable between turns and the
 * cached-selector comparison skips the re-render.
 *
 * Streaming text is deliberately excluded. Reading it would extract paths from a
 * half-typed reply, so a filename would appear character by character and change
 * shape as it lands. The previous turn's list stays up — an honest stale value —
 * until the new reply commits.
 */
export function useLastReply(sessionId: string | null): ApiMessage | null {
  return useStoreSelector((s) => {
    const messages = sessionId ? s.conversations[sessionId]?.messages : undefined;
    if (!messages) return null;
    for (let i = messages.length - 1; i >= 0; i -= 1) {
      const message = messages[i];
      if (message.role !== "assistant") continue;
      // Same rule the conversation uses to decide where the action bar goes:
      // trust stop_reason, and fall back to structure for salvaged or
      // pre-migration history (see lib/message-grouping.ts).
      if (isFinalReply(message, messages[i + 1])) return message;
    }
    return null;
  });
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

export function useTranscriptState(id: string | null) {
  return useStoreSelector((s) => {
    const c = id ? s.conversations[id] ?? EMPTY_CONVO : EMPTY_CONVO;
    return { messages: c.messages, loading: c.loading, historyLoaded: c.historyLoaded, hasOlder: c.hasOlder, hasNewer: c.hasNewer, error: c.error, latestFinalID: c.latestFinalID };
  }, shallowEqual);
}
