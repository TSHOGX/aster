import { describe, expect, it } from "vitest";

import { emptyConvo, withActivity } from "./harness-events";
import { reduceRuntimeActivity, shouldApplyRuntime } from "./session-runtime";
import type { ApiSessionRuntime, ApiTurn } from "./types";

function turn(overrides: Partial<ApiTurn> = {}): ApiTurn {
  return {
    id: 1,
    sequence: 1,
    content: "do the thing",
    status: "running",
    created_at: "2026-09-11T00:00:00Z",
    ...overrides
  };
}

function snapshot(overrides: Partial<ApiSessionRuntime> = {}): ApiSessionRuntime {
  return {
    session_id: "sess_1",
    session_status: "running_active",
    revision: 1,
    active_turn: null,
    queued_turns: [],
    ...overrides
  };
}

describe("shouldApplyRuntime", () => {
  it("applies the first snapshot", () => {
    expect(shouldApplyRuntime(null, snapshot())).toBe(true);
    expect(shouldApplyRuntime(undefined, snapshot())).toBe(true);
  });

  it("applies a newer revision", () => {
    expect(shouldApplyRuntime(snapshot({ revision: 4 }), snapshot({ revision: 5 }))).toBe(true);
  });

  // The bug this guard exists for: a GET issued before a change resolves after
  // the push that reported it. Applying that older body would restore a queue
  // entry the server already drained, and nothing would contradict it again — so
  // the stale row would sit in the queue forever.
  it("drops a snapshot that is not newer than the one held", () => {
    const held = snapshot({ revision: 7, queued_turns: [] });
    const stale = snapshot({ revision: 6, queued_turns: [turn({ id: 9, status: "queued" })] });
    expect(shouldApplyRuntime(held, stale)).toBe(false);
    // Equal revisions carry identical state, so re-applying is pointless churn.
    expect(shouldApplyRuntime(held, snapshot({ revision: 7 }))).toBe(false);
  });

  it("ignores a payload with no revision", () => {
    const malformed = { ...snapshot(), revision: undefined } as unknown as ApiSessionRuntime;
    expect(shouldApplyRuntime(null, malformed)).toBe(false);
  });
});

describe("reduceRuntimeActivity", () => {
  it("adopts the phase the server recorded for the active turn", () => {
    const next = reduceRuntimeActivity(
      emptyConvo(),
      snapshot({ active_turn: turn({ phase: "tool", tool_name: "bash", tool_detail: "go test ./..." }) })
    );
    expect(next.activity.phase).toBe("tool");
    expect(next.activity.toolName).toBe("bash");
    expect(next.activity.toolDetail).toBe("go test ./...");
  });

  it("treats an active turn with no recorded phase as thinking", () => {
    // A leased turn has been handed to the bridge but has not acked start, so the
    // strip must show work rather than going blank during handoff.
    const next = reduceRuntimeActivity(emptyConvo(), snapshot({ active_turn: turn({ status: "leased" }) }));
    expect(next.activity.phase).toBe("thinking");
  });

  // The reported bug: an idle session displaying "Thinking". A session stays
  // running_active while a generation warms up, so the session status cannot
  // decide this — the absence of an active turn does.
  it("parks activity at idle when no turn is active, whatever the session says", () => {
    const busy = withActivity(emptyConvo(), { phase: "tool", toolName: "bash" });
    const next = reduceRuntimeActivity(busy, snapshot({ session_status: "running_active", active_turn: null }));
    expect(next.activity.phase).toBe("idle");
    expect(next.activity.toolName).toBeNull();
    expect(next.activity.startedAt).toBeNull();
  });

  it("drops streaming text once the turn is no longer active", () => {
    const streaming = { ...emptyConvo(), streaming: [{ id: "m1", text: "half a sentence" }] };
    const next = reduceRuntimeActivity(streaming, snapshot({ active_turn: null }));
    expect(next.streaming).toEqual([]);
  });

  it("keeps streaming text while the turn is still running", () => {
    const streaming = { ...emptyConvo(), streaming: [{ id: "m1", text: "half a sentence" }] };
    const next = reduceRuntimeActivity(streaming, snapshot({ active_turn: turn({ phase: "streaming" }) }));
    expect(next.streaming).toHaveLength(1);
    expect(next.activity.phase).toBe("streaming");
  });

  // A queued turn is pending work, not running work: it must not produce an
  // activity phase of its own, or the strip would claim the agent is thinking
  // about a prompt it has not been given.
  it("stays idle when turns are queued but none is active", () => {
    const next = reduceRuntimeActivity(
      emptyConvo(),
      snapshot({ active_turn: null, queued_turns: [turn({ id: 2, status: "queued" })] })
    );
    expect(next.activity.phase).toBe("idle");
  });
});
