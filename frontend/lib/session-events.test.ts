import { describe, expect, it } from "vitest";

import { reconcileSessionList, mergeSessionList, reduceSessionEvent, type SessionEventSlice } from "./session-events";
import type { ApiSession, HarnessEvent } from "./types";

type TestConversation = {
  streaming: Array<{ id: string; text: string }>;
};

function session(patch: Partial<ApiSession> = {}): ApiSession {
  return {
    id: "sess_1",
    user_id: "lab",
    status: "paused",
    mode: "agent",
    mode_label: "Agent",
    created_at: "2026-05-26T00:00:00Z",
    updated_at: "2026-05-26T00:00:00Z",
    last_activity_at: "2026-05-26T00:00:00Z",
    ...patch
  };
}

function slice(baseSession = session()): SessionEventSlice<TestConversation> {
  return {
    sessions: [baseSession],
    conversations: {
      sess_1: { streaming: [{ id: "pending", text: "partial" }] }
    }
  };
}

function reduce(base: SessionEventSlice<TestConversation>, event: HarnessEvent) {
  return reduceSessionEvent(base, event, {
    emptyConversation: () => ({ streaming: [] }),
    time: "2026-05-26T01:00:00Z"
  });
}

describe("reduceSessionEvent", () => {
  it("keeps retryable generation errors input-acceptable and clears streaming", () => {
    const next = reduce(slice(session({ status: "running_active" })), {
      type: "generation.error",
      session_id: "sess_1",
      payload: {
        error: "runtime failed",
        session_status: "running_idle",
        session_updated_at: "2026-05-26T01:01:00Z"
      }
    });

    expect(next.sessions[0].status).toBe("running_idle");
    expect(next.sessions[0].updated_at).toBe("2026-05-26T01:01:00Z");
    expect(next.conversations.sess_1.streaming).toEqual([]);
    expect(next.notifications).toEqual([{ level: "error", message: "runtime failed", errorClass: undefined }]);
  });

  it("carries the generation error class so the UI can word known faults", () => {
    const next = reduce(slice(session({ status: "running_idle" })), {
      type: "generation.error",
      session_id: "sess_1",
      payload: {
        error: "sandbox process exited (exit status 1): FileNotFoundError: ...",
        error_class: "sandbox_exited",
        session_status: "running_idle",
        session_updated_at: "2026-09-12T05:00:00Z"
      }
    });
    expect(next.notifications).toEqual([
      {
        level: "error",
        message: "sandbox process exited (exit status 1): FileNotFoundError: ...",
        errorClass: "sandbox_exited"
      }
    ]);
  });

  it("marks terminal session events failed", () => {
    const failedStatus = reduce(slice(session({ status: "running_idle" })), {
      type: "session.failed",
      session_id: "sess_1",
      time: "2026-05-26T01:04:00Z"
    });
    expect(failedStatus.sessions[0].status).toBe("failed");
    expect(failedStatus.conversations.sess_1.streaming).toEqual([]);

    const terminalError = reduce(slice(session({ status: "running_idle" })), {
      type: "session.error",
      session_id: "sess_1",
      payload: { terminal: true, error: "terminal failure" }
    });
    expect(terminalError.sessions[0].status).toBe("failed");
    expect(terminalError.notifications).toEqual([{ level: "error", message: "terminal failure" }]);
  });

  it("does not fail the session for non-terminal session errors or failed turns", () => {
    const nonTerminal = reduce(slice(session({ status: "running_idle" })), {
      type: "session.error",
      session_id: "sess_1",
      payload: { terminal: false, error: "retryable runtime error" }
    });
    expect(nonTerminal.sessions[0].status).toBe("running_idle");
    expect(nonTerminal.conversations.sess_1.streaming).toEqual([]);

    const failedTurn = reduce(slice(session({ status: "running_active" })), {
      type: "ack_turn_completed",
      session_id: "sess_1",
      payload: {
        status: "failed",
        error: "turn failed",
        session_status: "running_idle",
        session_updated_at: "2026-05-26T01:05:00Z"
      }
    });
    expect(failedTurn.sessions[0].status).toBe("running_idle");
    expect(failedTurn.notifications).toEqual([{ level: "error", message: "turn failed" }]);
  });
});

describe("mergeSessionList", () => {
  function merge(base: SessionEventSlice<TestConversation>, sessions: ApiSession[]) {
    return mergeSessionList(base, sessions, { emptyConversation: () => ({ streaming: [] }) });
  }

  it("back-fills conversations for newly visible sessions and keeps loaded ones", () => {
    const base = slice();
    const shared = session({ id: "sess_2", access: "reader", shared_by: "alice", user_id: undefined });
    const next = merge(base, [session(), shared]);

    expect(next.sessions.map((s) => s.id)).toEqual(["sess_1", "sess_2"]);
    expect(next.conversations.sess_2).toEqual({ streaming: [] });
    // Existing conversation state must survive the swap untouched.
    expect(next.conversations.sess_1).toBe(base.conversations.sess_1);
  });

  it("replaces the list wholesale and keeps the conversations map identity when nothing is new", () => {
    const base = slice();
    const next = merge(base, [session({ title: "renamed" })]);

    expect(next.sessions).toEqual([session({ title: "renamed" })]);
    expect(next.conversations).toBe(base.conversations);
  });

  it("keeps conversations of sessions that dropped out of the list", () => {
    const base = slice();
    const next = merge(base, []);

    expect(next.sessions).toEqual([]);
    expect(next.conversations.sess_1).toBe(base.conversations.sess_1);
  });
});

describe("reconcileSessionList", () => {
  it("does not roll a runtime push back when an older GET completes", () => {
    const before = session({ sandbox: "live" });
    const pushed = { ...before, sandbox: "released" as const };
    expect(reconcileSessionList([before], [pushed], [before])).toEqual([pushed]);
  });

  it("refreshes unchanged rows and preserves concurrent creations/deletions", () => {
    const before = session();
    const created = session({ id: "new" });
    expect(reconcileSessionList([before], [created], [before])).toEqual([created]);
    const fetched = { ...before, sandbox: "released" as const };
    expect(reconcileSessionList([before], [before], [fetched])).toEqual([fetched]);
    expect(reconcileSessionList([before], [before], [])).toEqual([]);
  });
});
