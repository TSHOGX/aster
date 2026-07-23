import { describe, expect, it } from "vitest";

import { isFinalReply } from "./message-grouping";
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
});
