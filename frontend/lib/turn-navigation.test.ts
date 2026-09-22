import { expect, it } from "vitest";
import {
  boundedTurnIndex,
  mergeTurnIndex,
  turnWaveWidth,
} from "./turn-navigation";
it("creates a symmetric wave with its maximum on the hovered turn", () => {
  expect(turnWaveWidth(4, 4)).toBe(30);
  expect(turnWaveWidth(3, 4)).toBe(turnWaveWidth(5, 4));
  expect(turnWaveWidth(3, 4)).toBeGreaterThan(turnWaveWidth(2, 4));
  expect(turnWaveWidth(0, null)).toBe(9);
});

it("bounds both paging directions and retains cursors for evicted edges", () => {
  const rows = (first: number, last: number) =>
    Array.from({ length: last - first + 1 }, (_, i) => ({
      id: `t${first + i}`,
      sequence: first + i,
      state: "completed",
      created_at: "",
      user_preview: "q".repeat(900),
      assistant_preview: "a".repeat(1500),
    }));
  const older = boundedTurnIndex(
    { turns: rows(701, 1000), next_before: 701 },
    { turns: rows(601, 700), next_before: 601, next_after: 700 },
    "older",
  );
  expect(older.turns).toHaveLength(300);
  expect(older.next_before).toBe(601);
  expect(older.next_after).toBe(900);
  expect(older.turns[0].user_preview).toHaveLength(600);
  expect(older.turns[0].assistant_preview).toHaveLength(1200);
  const newer = boundedTurnIndex(
    older,
    { turns: rows(901, 1000), next_before: 901 },
    "newer",
  );
  expect(newer.turns).toHaveLength(300);
  expect(newer.turns[0].sequence).toBe(701);
  expect(newer.next_before).toBe(701);
  expect(newer.next_after).toBeUndefined();
  expect(
    boundedTurnIndex(
      newer,
      { turns: rows(900, 1000), next_before: 900 },
      "replace",
    ).turns,
  ).toHaveLength(101);
});
it("appends older pages and updates completed previews without duplicates", () => {
  const t = (sequence: number, state = "running") => ({
    id: String(sequence),
    sequence,
    state,
    created_at: "",
    user_preview: "question",
    assistant_preview: state === "completed" ? "final" : "",
  });
  expect(
    mergeTurnIndex([t(2), t(3)], [t(1), t(2, "completed")]).map((x) => [
      x.sequence,
      x.assistant_preview,
    ]),
  ).toEqual([
    [1, ""],
    [2, "final"],
    [3, ""],
  ]);
});
