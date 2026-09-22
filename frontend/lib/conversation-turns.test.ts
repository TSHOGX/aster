import { expect, it } from "vitest";
import {
  conversationTurns,
  elapsedLabel,
  finalItems,
} from "./conversation-turns";
import type { TimelineItem } from "./project-api";
it("formats seconds, minutes and hours and freezes at server completion", () => {
  const start = "2026-09-22T00:00:00Z";
  expect(elapsedLabel(start, undefined, Date.parse(start) + 54000)).toBe("54s");
  expect(elapsedLabel(start, "2026-09-22T00:11:23Z")).toBe("11m 23s");
  expect(elapsedLabel(start, "2026-09-22T02:01:09Z")).toBe("2h 1m 9s");
  expect(elapsedLabel(start, "2026-09-22T00:00:54Z", 0)).toBe("54s");
  expect(elapsedLabel(undefined)).toBe("—");
});
it("requires confirmed completion and preserves all blocks of the final message", () => {
  const timeline: TimelineItem[] = [
    { id: "u", cursor: 1, turn_id: "t", kind: "user" },
    { id: "mid", cursor: 2, turn_id: "t", kind: "assistant", message_id: "m1" },
    {
      id: "thinking",
      cursor: 3,
      turn_id: "t",
      kind: "thinking",
      message_id: "m2",
    },
    {
      id: "final1",
      cursor: 3,
      turn_id: "t",
      kind: "assistant",
      message_id: "m2",
    },
    {
      id: "final2",
      cursor: 3,
      turn_id: "t",
      kind: "assistant",
      message_id: "m2",
    },
  ];
  const complete = conversationTurns(
    timeline,
    [{ id: "t", state: "completed", final_message_id: "m2" }],
    [],
  )[0];
  expect(finalItems(complete).map((i) => i.id)).toEqual(["final1", "final2"]);
  for (const state of ["running", "canceled", "failed", "unknown"])
    expect(
      finalItems({ ...complete, summary: { ...complete.summary!, state } }),
    ).toEqual([]);
});
it("preserves interleaved steering turn boundaries and old-page metadata", () => {
  const items = ["t", "steer", "t"].map((turn_id, i) => ({
    id: String(i),
    cursor: i,
    turn_id,
    kind: "assistant",
  }));
  const groups = conversationTurns(
    items,
    [
      {
        id: "t",
        state: "completed",
        created_at: "2026-09-22T00:00:00Z",
        finished_at: "2026-09-22T00:00:03Z",
      },
    ],
    [],
  );
  expect(groups.map((t) => t.turnId)).toEqual(["t", "steer", "t"]);
  expect(groups[0].summary?.finished_at).toBe("2026-09-22T00:00:03Z");
});
