import { describe, expect, it } from "vitest";
import {
  parseRecurrence,
  recurrenceCron,
  scheduleConfiguration,
  scheduleTime,
} from "./schedule-recurrence";
import type { Schedule } from "./project-api";

describe("visual recurrence", () => {
  it.each([
    "0 9 * * *",
    "0 9 * * 1-5",
    "0 16 * * 1,3,5",
    "0 9 31 * *",
    "0 9 1,15 * *",
    "@every 1m",
    "@every 90m",
    "@every 10080m",
    "",
  ])("round trips supported rule %s", (rule) => {
    expect(recurrenceCron(parseRecurrence(rule))).toBe(rule);
  });
  it.each([
    "*/15 9-17 * * MON-FRI",
    "0 9 1 * 1",
    "0 9 * JAN *",
    "0 9 * * 0-6",
    "0 9 * * 7",
  ])("keeps complex rule exact in advanced mode: %s", (rule) => {
    const parsed = parseRecurrence(rule);
    expect(parsed.mode).toBe("advanced");
    expect(recurrenceCron(parsed)).toBe(rule);
  });
  it("rejects missing days and invalid wall time", () => {
    const rule = parseRecurrence("0 9 * * *");
    expect(
      recurrenceCron({ ...rule, mode: "weekly", weekdays: [] }),
    ).toBeNull();
    expect(recurrenceCron({ ...rule, time: "24:00" })).toBeNull();
    expect(recurrenceCron({ ...rule, mode: "dates", dates: [] })).toBeNull();
  });
  it("validates whole minute intervals without inventing Cron steps", () => {
    const rule = {
      ...parseRecurrence("@every 90m"),
      mode: "interval" as const,
    };
    expect(rule.intervalMinutes).toBe("90");
    expect(recurrenceCron(rule)).toBe("@every 90m");
    for (const intervalMinutes of ["", "0", "-1", "1.5", "10081", "1e3"])
      expect(recurrenceCron({ ...rule, intervalMinutes })).toBeNull();
    expect(parseRecurrence("*/15 * * * *").mode).toBe("advanced");
  });
  it("separates configuration from scheduler-maintained revisions", () => {
    const original = {
      project_id: "p",
      id: "s",
      title: "title",
      prompt: "private",
      cron: "0 9 * * *",
      timezone: "UTC",
      state: "active", trigger_kind: "cron", notify: true,

      revision: 1,
    } as Schedule;
    expect(
      scheduleConfiguration({ ...original, revision: 2, next_at: "later" }),
    ).toBe(scheduleConfiguration(original));
    expect(scheduleConfiguration({ ...original, prompt: "changed" })).not.toBe(
      scheduleConfiguration(original),
    );
  });
  it("shows the offset for both occurrences of a repeated local time", () => {
    expect(
      scheduleTime("2026-11-01T05:30:00Z", "America/New_York", "en"),
    ).toContain("GMT-4");
    expect(
      scheduleTime("2026-11-01T06:30:00Z", "America/New_York", "en"),
    ).toContain("GMT-5");
  });
});
