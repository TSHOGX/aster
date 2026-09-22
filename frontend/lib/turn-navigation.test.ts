import { expect, it } from "vitest";
import { mergeTurnIndex, turnWaveWidth } from "./turn-navigation";
it("creates a symmetric wave with its maximum on the hovered turn", () => {
  expect(turnWaveWidth(4, 4)).toBe(30);
  expect(turnWaveWidth(3, 4)).toBe(turnWaveWidth(5, 4));
  expect(turnWaveWidth(3, 4)).toBeGreaterThan(turnWaveWidth(2, 4));
  expect(turnWaveWidth(0, null)).toBe(9);
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
