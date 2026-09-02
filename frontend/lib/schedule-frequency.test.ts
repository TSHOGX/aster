import { describe, expect, it } from "vitest";

import {
  buildCronExpression,
  frequencyDescription,
  parseCron,
  TIME_OPTIONS,
  type ScheduleFrequencyForm
} from "./schedule-frequency";

const base: ScheduleFrequencyForm = {
  frequency: "manual",
  intervalMinutes: 15,
  time: "08:00",
  weekday: 1,
  customRepeat: "hourly",
  customInterval: 1,
  customMinute: 0,
  customWeekday: 1,
  customDay: 1,
  customMonth: 1,
  customRawCron: ""
};

describe("schedule frequency mapping", () => {
  it("offers every quarter-hour of the day", () => {
    expect(TIME_OPTIONS).toHaveLength(96);
    expect(TIME_OPTIONS[0]?.value).toBe("00:00");
    expect(TIME_OPTIONS.at(-1)?.value).toBe("23:45");
  });

  it("builds the common frequency expressions", () => {
    expect(buildCronExpression({ ...base, frequency: "interval", intervalMinutes: 20 })).toBe("*/20 * * * *");
    expect(buildCronExpression({ ...base, frequency: "daily", time: "07:15" })).toBe("15 7 * * *");
    expect(buildCronExpression({ ...base, frequency: "weekdays", time: "09:00" })).toBe("0 9 * * 1-5");
    expect(buildCronExpression({ ...base, frequency: "weekly", weekday: 0, time: "18:45" })).toBe("45 18 * * 0");
  });

  it("builds custom hourly, monthly and yearly expressions", () => {
    expect(buildCronExpression({ ...base, frequency: "custom", customRepeat: "hourly", customInterval: 3, customMinute: 10 })).toBe("10 */3 * * *");
    expect(buildCronExpression({ ...base, frequency: "custom", customRepeat: "monthly", customInterval: 2, customDay: 12, time: "06:30" })).toBe("30 6 12 */2 *");
    expect(buildCronExpression({ ...base, frequency: "custom", customRepeat: "yearly", customMonth: 12, customDay: 31, time: "23:45" })).toBe("45 23 31 12 *");
  });

  it("round-trips supported expressions into form values", () => {
    expect(parseCron("0 8 * * 1-5")).toMatchObject({ frequency: "weekdays", time: "08:00" });
    expect(parseCron("15 */4 * * *")).toMatchObject({ frequency: "custom", customRepeat: "hourly", customInterval: 4, customMinute: 15 });
    expect(parseCron("0 9 * * 7")).toMatchObject({ frequency: "weekly", weekday: 0, time: "09:00" });
    expect(frequencyDescription("*/30 * * * *")).toBe("每隔 30 分钟");
  });

  it("preserves an unsupported legacy expression until the user edits frequency", () => {
    const parsed = parseCron("0 8 1,15 * *");
    expect(parsed).toMatchObject({ frequency: "custom", customRawCron: "0 8 1,15 * *" });
    expect(buildCronExpression({ ...base, ...parsed })).toBe("0 8 1,15 * *");
  });
});
