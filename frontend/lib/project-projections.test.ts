import { describe, expect, it } from "vitest";
import {
  acceptRecords,
  acceptSnapshot,
  emptySessionView,
  uniqueMessages,
} from "./project-projections";
import type { AgentRecord, SessionSnapshot } from "./project-api";
const snapshot = (revision: number, id = "agent"): SessionSnapshot => ({
  session: {
    id,
    project_id: "project",
    title: id,
    state: "running",
    revision,
    deletion: 0,
    control_revision: revision,
  },
  messages: [],
  turns: [
    {
      id: "turn",
      session_id: id,
      content: "work",
      state: "running",
      sequence: 1,
    },
  ],
  record_cursor: 5,
  has_earlier: false,
});
describe("Project projections", () => {
  it("rejects stale or foreign Session snapshots without conflating child revisions", () => {
    const current = acceptSnapshot(emptySessionView(), snapshot(8));
    expect(acceptSnapshot(current, snapshot(7))).toBe(current);
    expect(acceptSnapshot(current, snapshot(9, "sibling"))).toBe(current);
    expect(
      acceptSnapshot(current, {
        ...snapshot(10),
        session: { ...snapshot(10).session, project_id: "other" },
      }),
    ).toBe(current);
    expect(
      acceptSnapshot(current, snapshot(9)).snapshot?.session.revision,
    ).toBe(9);
  });
  it("deduplicates native replay and bounds provisional text and traces", () => {
    const current = acceptSnapshot(emptySessionView(), snapshot(1));
    const delta: AgentRecord = {
      cursor: 6,
      turn_id: "turn",
      kind: "native",
      payload: {
        type: "message_update",
        assistantMessageEvent: { type: "text_delta", delta: "hello" },
      },
    };
    const next = acceptRecords(current, [delta, delta]);
    expect(next.partial).toBe("hello");
    expect(acceptRecords(next, [delta]).partial).toBe("hello");
    expect(
      acceptRecords(next, [
        {
          ...delta,
          cursor: 7,
          payload: {
            type: "message_update",
            assistantMessageEvent: {
              type: "text_delta",
              delta: "x".repeat(100000),
            },
          },
        },
      ]).partial.length,
    ).toBe(65536);
    const tools = Array.from(
      { length: 200 },
      (_, i): AgentRecord => ({
        cursor: 10 + i,
        turn_id: "turn",
        kind: "native",
        payload: {
          type: "tool_execution_start",
          toolCallId: String(i),
          toolName: "read",
          args: { file: "data" },
        },
      }),
    );
    expect(acceptRecords(next, tools).tools).toHaveLength(50);
  });
  it("keeps committed messages canonical after a turn settles", () => {
    let view = acceptSnapshot(emptySessionView(), snapshot(1));
    view = { ...view, partial: "unfinished text" };
    view = acceptSnapshot(view, { ...snapshot(2), turns: [] });
    expect(view.partial).toBe("");
    expect(
      uniqueMessages([
        {
          id: "m",
          role: "user",
          content: "text",
          sequence: 1,
          created_at: "now",
        },
        {
          id: "m",
          role: "user",
          content: "text",
          sequence: 1,
          created_at: "now",
        },
      ]),
    ).toHaveLength(1);
  });
});

it("restores bounded tool activity and ignores old text replay after completion", () => {
  const next = {
    ...snapshot(3),
    turns: [],
    records: [
      {
        cursor: 3,
        turn_id: "old",
        kind: "native",
        payload: {
          type: "tool_execution_end",
          toolCallId: "call",
          toolName: "bash",
          result: { output: "done" },
        },
      },
    ],
  };
  const view = acceptSnapshot(emptySessionView(), next);
  expect(view.tools).toHaveLength(1);
  const replay = acceptRecords(view, [
    {
      cursor: 6,
      turn_id: "old",
      kind: "native",
      payload: {
        type: "message_update",
        assistantMessageEvent: { type: "text_delta", delta: "historical text" },
      },
    },
  ]);
  expect(replay.partial).toBe("");
});
