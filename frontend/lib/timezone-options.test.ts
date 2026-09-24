import { describe, expect, it } from "vitest";
import { timezoneOptions } from "./timezone-options";

describe("timezone choices", () => {
  it("includes UTC and retains a saved IANA alias without silently renaming it", () => {
    const options = timezoneOptions(
      "en",
      "Asia/Kolkata",
      new Date("2026-07-01T00:00:00Z"),
    );
    expect(options[0].value).toBe("UTC");
    expect(
      options.filter((option) => option.value === "Asia/Kolkata"),
    ).toHaveLength(1);
    expect(
      options.find((option) => option.value === "Asia/Kolkata")?.description,
    ).toContain("05:30");
    expect(new Set(options.map((option) => option.value)).size).toBe(
      options.length,
    );
  });
  it("labels current offsets across daylight saving and localizes zone names", () => {
    const winter = timezoneOptions(
      "en",
      "America/New_York",
      new Date("2026-01-01T00:00:00Z"),
    );
    const summer = timezoneOptions(
      "en",
      "America/New_York",
      new Date("2026-07-01T00:00:00Z"),
    );
    expect(
      winter.find((option) => option.value === "America/New_York")?.description,
    ).toContain("-05:00");
    expect(
      summer.find((option) => option.value === "America/New_York")?.description,
    ).toContain("-04:00");
    const chinese = timezoneOptions(
      "zh",
      "Asia/Shanghai",
      new Date("2026-07-01T00:00:00Z"),
    );
    expect(
      chinese.find((option) => option.value === "Asia/Shanghai")?.description,
    ).toContain("中国");
  });
  it("does not offer an invalid saved timezone as a selectable choice", () => {
    expect(
      timezoneOptions("en", "Invalid/Zone", new Date()).some(
        (option) => option.value === "Invalid/Zone",
      ),
    ).toBe(false);
  });
});
