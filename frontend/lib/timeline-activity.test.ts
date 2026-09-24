import { expect, it } from "vitest";
import {
  activityPreview,
  activityResult,
  groupTimeline,
} from "./timeline-activity";
import type { TimelineItem } from "./project-api";
const item = (
  id: string,
  kind: string,
  state = "completed",
  turn = "t",
): TimelineItem => ({ id, kind, state, turn_id: turn, run_id: "run", cursor: 1 });
it("groups adjacent tools and thinking, never across prose or turns", () => {
  const rows = groupTimeline(
    [
      item("a", "thinking"),
      item("b", "tool"),
      item("c", "thinking"),
      item("d", "assistant"),
      item("e", "tool"),
      item("f", "tool", "completed", "other"),
    ],
    new Set(),
  );
  expect(rows.map((x) => x.id)).toEqual(["a", "d", "e", "f"]);
  expect(rows[0]).toMatchObject({
    kind: "activity",
    items: [{ id: "a" }, { id: "b" }, { id: "c" }],
  });
});
it("keeps the group identity when streaming completes or another tool arrives", () => {
  const start = groupTimeline(
    [item("a", "thinking", "streaming")],
    new Set(["run"]),
  );
  const end = groupTimeline(
    [item("a", "thinking"), item("b", "tool")],
    new Set(),
  );
  expect(start[0].id).toBe(end[0].id);
  expect(start[0]).toMatchObject({ live: { id: "a" } });
  expect(end[0]).toMatchObject({ live: undefined });
});
it("does not animate old thinking when the same native message streams prose, or a stopped turn", () => {
  expect(
    groupTimeline(
      [item("a", "thinking", "streaming"), item("b", "assistant", "streaming")],
      new Set(["run"]),
    )[0],
  ).toMatchObject({ live: undefined });
  expect(
    groupTimeline([item("a", "tool", "running")], new Set())[0],
  ).toMatchObject({ live: undefined });
});
it("previews tool commands without output noise and the latest thinking tail", () => {
  expect(
    activityPreview({
      ...item("a", "tool"),
      arguments: JSON.stringify({
        command: "python\nheart.py",
        content: "large file body",
      }),
    }),
  ).toBe("python heart.py");
  expect(
    activityPreview({
      ...item("b", "thinking"),
      content: "x".repeat(300) + " latest thought",
    }),
  ).toHaveLength(200);
  expect(
    activityPreview({
      ...item("c", "tool"),
      arguments: '{"path":"/workspace/heart.py"}',
    }),
  ).toBe("/workspace/heart.py");
});

it("keeps every pending and running call live, including calls before prose", () => {
  const rows = [
    item("a", "tool", "pending"),
    item("b", "tool", "running"),
    item("c", "tool"),
    item("d", "assistant"),
  ];
  expect(groupTimeline(rows, new Set(["run"]))[0]).toMatchObject({
    active: [{ id: "a" }, { id: "b" }],
  });
  expect(groupTimeline(rows, new Set())[0]).toMatchObject({
    active: [],
    live: undefined,
  });
});

it("keeps a steered segment's tool active under the original run", () => {
  const tool = item("steered-tool", "tool", "running", "steer-source");
  expect(groupTimeline([tool], new Set(["run"]))[0]).toMatchObject({
    live: { id: "steered-tool" },
  });
  expect(groupTimeline([tool], new Set(["another-run"]))[0]).toMatchObject({
    live: undefined,
  });
});

it("renders text results readably and keeps structured or partial results inspectable", () => {
  expect(
    activityResult('{"content":[{"type":"text","text":"line 1\\nline 2"}]}'),
  ).toBe("line 1\nline 2");
  expect(activityResult('{"rows":12}')).toBe('{\n  "rows": 12\n}');
  expect(activityResult('{"content": [')).toBe('{"content": [');
});
