import { isRecord } from "./guards";
import type { ApiSession, HarnessEvent, SessionStatus } from "./types";

export type SessionEventConversation = {
  streaming: unknown[];
};

export type SessionEventSlice<TConversation extends SessionEventConversation> = {
  sessions: ApiSession[];
  conversations: Record<string, TConversation>;
};

export type SessionEventNotification = {
  level: "error";
  message: string;
  /** Server error class, when the payload had one; lets the UI word known faults itself. */
  errorClass?: string;
};

export type SessionEventReduction<TConversation extends SessionEventConversation> =
  SessionEventSlice<TConversation> & {
    handled: boolean;
    notifications: SessionEventNotification[];
  };

const SESSION_EVENT_STATUSES = new Set<SessionStatus>([
  "running_active",
  "running_idle",
  "pausing",
  "paused",
  "failed",
  "destroyed"
]);

function readSessionStatusEvent(type: string): SessionStatus | null {
  if (!type.startsWith("session.")) return null;
  const status = type.slice("session.".length) as SessionStatus;
  return SESSION_EVENT_STATUSES.has(status) ? status : null;
}

function readSessionStatus(value: unknown): SessionStatus | null {
  return typeof value === "string" && SESSION_EVENT_STATUSES.has(value as SessionStatus)
    ? (value as SessionStatus)
    : null;
}

/**
 * Error classes that describe the user's own control action rather than a fault.
 *
 * Stop tears the generation down, so the server correctly emits a
 * generation/session error for it — but showing that as a red toast reports the
 * user's own click back to them as a failure.
 */
const USER_INITIATED_ERROR_CLASSES = new Set(["user_interrupted", "user_canceled"]);

function isUserInitiated(payload: Record<string, unknown>): boolean {
  return (
    typeof payload.error_class === "string" && USER_INITIATED_ERROR_CLASSES.has(payload.error_class)
  );
}

function updateSession(
  sessions: ApiSession[],
  sessionId: string,
  patch: (session: ApiSession) => ApiSession
) {
  return sessions.map((session) => (session.id === sessionId ? patch(session) : session));
}

function clearStreaming<TConversation extends SessionEventConversation>(
  conversations: Record<string, TConversation>,
  sessionId: string,
  emptyConversation: () => TConversation
) {
  const current = conversations[sessionId] ?? emptyConversation();
  return {
    ...conversations,
    [sessionId]: { ...current, streaming: [] }
  };
}

function emptyReduction<TConversation extends SessionEventConversation>(
  slice: SessionEventSlice<TConversation>,
  handled = false
): SessionEventReduction<TConversation> {
  return { ...slice, handled, notifications: [] };
}

/** Preserve changes delivered while an unversioned list request was in flight. */
export function reconcileSessionList(
  before: ApiSession[],
  current: ApiSession[],
  incoming: ApiSession[]
): ApiSession[] {
  const baseline = new Map(before.map((session) => [session.id, session]));
  const held = new Map(current.map((session) => [session.id, session]));
  const received = new Set(incoming.map((session) => session.id));
  const result = incoming.flatMap((session) => {
    const existing = held.get(session.id);
    if (baseline.has(session.id) && !existing) return []; // removed locally
    return [existing && existing !== baseline.get(session.id) ? existing : session];
  });
  return [
    ...current.filter((session) => !received.has(session.id) && session !== baseline.get(session.id)),
    ...result
  ];
}

/**
 * Swap in a freshly fetched session list while preserving the invariant that
 * every listed session has a conversations entry (callers index that map by
 * session id, so a session without one would read undefined). Conversation
 * state already loaded is left untouched, and the map keeps its identity when
 * nothing had to be back-filled so background refetches don't churn renders.
 */
export function mergeSessionList<TConversation extends SessionEventConversation>(
  slice: SessionEventSlice<TConversation>,
  sessions: ApiSession[],
  options: { emptyConversation: () => TConversation }
): SessionEventSlice<TConversation> {
  let conversations = slice.conversations;
  for (const session of sessions) {
    if (conversations[session.id]) continue;
    if (conversations === slice.conversations) conversations = { ...conversations };
    conversations[session.id] = options.emptyConversation();
  }
  return { sessions, conversations };
}

export function reduceSessionEvent<TConversation extends SessionEventConversation>(
  slice: SessionEventSlice<TConversation>,
  event: HarnessEvent,
  options: { emptyConversation: () => TConversation; time: string }
): SessionEventReduction<TConversation> {
  const sessionId = event.session_id;
  const time = event.time ?? options.time;

  const status = readSessionStatusEvent(event.type);
  if (status) {
    if (!sessionId) return emptyReduction(slice, true);
    return {
      sessions: updateSession(slice.sessions, sessionId, (session) => ({ ...session, status, updated_at: time })),
      conversations:
        status === "running_active"
          ? slice.conversations
          : clearStreaming(slice.conversations, sessionId, options.emptyConversation),
      handled: true,
      notifications: []
    };
  }

  switch (event.type) {
    case "session.error": {
      if (!isRecord(event.payload)) return emptyReduction(slice, true);
      const message = typeof event.payload.error === "string" ? event.payload.error : "Session failed";
      const userInitiated = isUserInitiated(event.payload);
      if (!sessionId) return { ...emptyReduction(slice, true), notifications: [{ level: "error", message }] };
      const terminal = event.payload.terminal === true;
      return {
        sessions: terminal
          ? updateSession(slice.sessions, sessionId, (session) => ({ ...session, status: "failed", updated_at: time }))
          : slice.sessions,
        conversations: clearStreaming(slice.conversations, sessionId, options.emptyConversation),
        handled: true,
        notifications: userInitiated ? [] : [{ level: "error", message }]
      };
    }
    case "generation.error": {
      if (!sessionId || !isRecord(event.payload)) return emptyReduction(slice, true);
      const message = typeof event.payload.error === "string" ? event.payload.error : "Runtime start failed";
      const errorClass = typeof event.payload.error_class === "string" ? event.payload.error_class : undefined;
      const userInitiated = isUserInitiated(event.payload);
      const sessionStatus = readSessionStatus(event.payload.session_status);
      const updatedAt =
        typeof event.payload.session_updated_at === "string" ? event.payload.session_updated_at : time;
      return {
        sessions: sessionStatus
          ? updateSession(slice.sessions, sessionId, (session) => ({
              ...session,
              status: sessionStatus,
              updated_at: updatedAt
            }))
          : slice.sessions,
        conversations: clearStreaming(slice.conversations, sessionId, options.emptyConversation),
        handled: true,
        notifications: userInitiated ? [] : [{ level: "error", message, errorClass }]
      };
    }
    case "ack_turn_completed": {
      if (!sessionId || !isRecord(event.payload)) return emptyReduction(slice, true);
      const turnStatus = typeof event.payload.status === "string" ? event.payload.status : "completed";
      const sessionStatus = readSessionStatus(event.payload.session_status);
      const userInitiated = isUserInitiated(event.payload);
      const updatedAt =
        typeof event.payload.session_updated_at === "string" ? event.payload.session_updated_at : time;
      const notifications: SessionEventNotification[] = [];
      if ((turnStatus === "failed" || turnStatus === "canceled") && !userInitiated) {
        const message =
          typeof event.payload.error === "string" && event.payload.error
            ? event.payload.error
            : turnStatus === "canceled"
              ? "Turn canceled"
              : "Turn failed";
        notifications.push({ level: "error", message });
      }
      return {
        sessions: sessionStatus
          ? updateSession(slice.sessions, sessionId, (session) => ({
              ...session,
              status: sessionStatus,
              updated_at: updatedAt
            }))
          : slice.sessions,
        conversations: clearStreaming(slice.conversations, sessionId, options.emptyConversation),
        handled: true,
        notifications
      };
    }
    default:
      return emptyReduction(slice);
  }
}
