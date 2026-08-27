import { describe, expect, it } from "vitest";

import { isFinalReply, interruptionKind, wasInterrupted } from "./message-grouping";
import type { ApiMessage } from "./types";

function msg(partial: Partial<ApiMessage>): ApiMessage {
  return {
    id: 1,
    session_id: "sess_x",
    role: "assistant",
    content: "",
    created_at: "2026-07-23T00:00:00Z",
    ...partial
  };
}

describe("isFinalReply", () => {
  it("never shows actions on user messages", () => {
    expect(isFinalReply(msg({ role: "user", stop_reason: "stop" }), undefined)).toBe(false);
  });

  it("trusts stop_reason=stop as final regardless of what follows", () => {
    const next = msg({ role: "assistant" });
    expect(isFinalReply(msg({ stop_reason: "stop" }), next)).toBe(true);
  });

  it("treats stop_reason=toolUse as an interstitial step", () => {
    expect(isFinalReply(msg({ stop_reason: "toolUse" }), undefined)).toBe(false);
  });

  it("falls back to structure when stop_reason is absent", () => {
    // Last message in the list => final.
    expect(isFinalReply(msg({ stop_reason: "" }), undefined)).toBe(true);
    // Followed by the user's next prompt => final.
    expect(isFinalReply(msg({}), msg({ role: "user" }))).toBe(true);
    // Followed by another assistant message => interstitial.
    expect(isFinalReply(msg({}), msg({ role: "assistant" }))).toBe(false);
  });

  it("does not offer actions on a failed model call", () => {
    // An error notice is the turn's last message but it is not a reply.
    expect(isFinalReply(msg({ stop_reason: "error" }), undefined)).toBe(false);
  });
});

describe("wasInterrupted", () => {
  it("is false for an empty conversation", () => {
    expect(wasInterrupted([])).toBe(false);
  });

  it("is false when the last turn answered", () => {
    expect(wasInterrupted([msg({ role: "user" }), msg({ stop_reason: "stop" })])).toBe(false);
  });

  it("detects a failed model call", () => {
    expect(wasInterrupted([msg({ role: "user" }), msg({ stop_reason: "error" })])).toBe(true);
  });

  it("detects a transcript that stops mid tool-loop", () => {
    // The production symptom: the agent announces a tool call and the turn dies
    // before the result comes back.
    expect(
      wasInterrupted([
        msg({ role: "user" }),
        msg({ stop_reason: "stop" }),
        msg({ role: "user" }),
        msg({ stop_reason: "toolUse" })
      ])
    ).toBe(true);
  });

  it("looks only at the most recent turn", () => {
    // An earlier interruption the user already moved past must not keep the
    // banner pinned to the bottom of a healthy conversation.
    expect(
      wasInterrupted([
        msg({ role: "user" }),
        msg({ stop_reason: "error" }),
        msg({ role: "user" }),
        msg({ stop_reason: "stop" })
      ])
    ).toBe(false);
  });

  it("is false when the user has the last word", () => {
    // A just-submitted prompt is not an interruption.
    expect(wasInterrupted([msg({ stop_reason: "stop" }), msg({ role: "user" })])).toBe(false);
  });

  it("stays quiet on unknown terminators", () => {
    // Shell output and pre-migration history carry no stop_reason; claiming
    // interruption there would show the banner on healthy sessions.
    expect(wasInterrupted([msg({ role: "user" }), msg({ stop_reason: "" })])).toBe(false);
  });

  it("detects a sandbox death", () => {
    expect(wasInterrupted([msg({ role: "user" }), msg({ stop_reason: "sandbox_died" })])).toBe(
      true
    );
  });
});

describe("interruptionKind", () => {
  it("reports undefined when the last turn answered", () => {
    expect(interruptionKind([msg({ role: "user" }), msg({ stop_reason: "stop" })])).toBeUndefined();
  });

  it("classifies a failed model call as recoverable in place", () => {
    // The sandbox is still alive and holds the agent's context, so "continue
    // where you left off" is accurate here.
    expect(interruptionKind([msg({ role: "user" }), msg({ stop_reason: "error" })])).toBe("model");
  });

  it("classifies a stalled tool loop as a model interruption", () => {
    expect(interruptionKind([msg({ role: "user" }), msg({ stop_reason: "toolUse" })])).toBe(
      "model"
    );
  });

  it("classifies a sandbox death separately", () => {
    // This is the distinction that matters: the agent's context did not survive,
    // so the UI must not offer to resume mid-thought.
    expect(interruptionKind([msg({ role: "user" }), msg({ stop_reason: "sandbox_died" })])).toBe(
      "sandbox"
    );
  });

  it("reports the most recent interruption when both kinds appear", () => {
    // A sandbox death after an earlier model failure must win: the older failure
    // is already resolved history.
    expect(
      interruptionKind([
        msg({ role: "user" }),
        msg({ stop_reason: "error" }),
        msg({ role: "user" }),
        msg({ stop_reason: "sandbox_died" })
      ])
    ).toBe("sandbox");
  });

  it("ignores an interruption the user already moved past", () => {
    expect(
      interruptionKind([
        msg({ role: "user" }),
        msg({ stop_reason: "sandbox_died" }),
        msg({ role: "user" }),
        msg({ stop_reason: "stop" })
      ])
    ).toBeUndefined();
  });
});
