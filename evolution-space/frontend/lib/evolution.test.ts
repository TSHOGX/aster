import { expect, it } from "vitest";
import { backfillInterval, budgetText, dependencySelection, parseBackfillBudgets, type Binding, type Budget } from "./evolution";
const budget: Budget = { max_tokens: 100, max_cost_micros: 100, max_attempts: 10, timeout_seconds: 60, max_artifact_bytes: 1024 };
const bindings = [{ id: "skills", enabled: true, budget, inputs: { corpus: { sources: ["source"] } }, depends_on: [] }, { id: "review", enabled: true, budget, inputs: { upstream: { dependency: { binding_id: "skills", output_port: "candidate" } } }, depends_on: ["skills"] }] as unknown as Binding[];
it("includes prerequisite budgets and refuses unavailable prerequisite bindings", () => {
  expect(dependencySelection(bindings, ["review"])).toEqual(["review", "skills"]);
  expect(Object.keys(parseBackfillBudgets(bindings, ["review"], {}))).toEqual(["review", "skills"]);
  expect(() => dependencySelection([{ ...bindings[0], enabled: false }, bindings[1]], ["review"])).toThrow("unavailable");
});
it("rejects imprecise, fractional, negative and over-ceiling budgets", () => {
  for (const value of ["101", "1.5", "-1", "1e2", "9007199254740992", ""]) expect(() => parseBackfillBudgets(bindings, ["skills"], { skills: { ...budgetText(budget), max_tokens: value } })).toThrow("whole number");
  expect(parseBackfillBudgets(bindings, ["skills"], { skills: { ...budgetText(budget), max_cost_micros: "0" } }).skills.max_cost_micros).toBe(0);
});
it("keeps explicit time-zone offsets and bounds the interval", () => {
  expect(backfillInterval("2026-10-15T08:00:00+08:00", "2026-10-15T09:00:00+08:00")).toEqual({ from: "2026-10-15T00:00:00.000Z", until: "2026-10-15T01:00:00.000Z" });
  expect(() => backfillInterval("2026-01-01", "2026-10-15")).toThrow("90 days");
  expect(() => backfillInterval("2026-10-15", "2026-10-14")).toThrow("90 days");
});
it("rejects an aggregate that cannot be represented exactly", () => {
  const oversized = bindings.map(binding => ({ ...binding, budget: { ...budget, max_tokens: Number.MAX_SAFE_INTEGER } }));
  expect(() => parseBackfillBudgets(oversized, ["review"], {})).toThrow("Combined budgets");
});
