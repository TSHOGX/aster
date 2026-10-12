import { describe, expect, it } from "vitest";

import { isChineseTag, resolveLocale } from "./locale";
import { format, translate } from "./index";
import { defaultDisplayName } from "../../../web/brand";
import { EN, type MessageKey } from "./messages-en";
import { ZH } from "./messages-zh";

describe("isChineseTag", () => {
  it("matches every Chinese variant, not an allow-list of regions", () => {
    for (const tag of [
      "zh",
      "zh-CN",
      "zh-TW",
      "zh-HK",
      "zh-Hans",
      "zh-Hant-HK",
      "zh-SG",
      "ZH-cn",
    ]) {
      expect(isChineseTag(tag), tag).toBe(true);
    }
  });

  it("tolerates the underscore form some environments emit", () => {
    expect(isChineseTag("zh_CN")).toBe(true);
  });

  it("rejects non-Chinese tags, including ones merely containing 'zh'", () => {
    for (const tag of ["en", "en-US", "ja", "ko", "de-CH", "azh", "en-zh"]) {
      expect(isChineseTag(tag), tag).toBe(false);
    }
  });
});

describe("resolveLocale", () => {
  it("returns zh when Chinese is the top preference", () => {
    expect(resolveLocale(["zh-CN", "en-US"])).toBe("zh");
  });

  it("returns en when English outranks Chinese", () => {
    // Position is the whole point: this user reads English first.
    expect(resolveLocale(["en-US", "zh-CN"])).toBe("en");
  });

  it("returns en for any non-Chinese browser", () => {
    expect(resolveLocale(["ja-JP"])).toBe("en");
    expect(resolveLocale(["de"])).toBe("en");
  });

  it("falls back to en for missing or empty input", () => {
    expect(resolveLocale(undefined)).toBe("en");
    expect(resolveLocale([])).toBe("en");
    expect(resolveLocale(["", "  "])).toBe("en");
  });

  it("skips blank entries before deciding", () => {
    expect(resolveLocale(["", "zh-CN"])).toBe("zh");
  });
});

describe("catalogs", () => {
  const enKeys = Object.keys(EN) as MessageKey[];

  it("translates every key in both locales", () => {
    for (const key of enKeys) {
      expect(EN[key], `EN ${key}`).toBeTruthy();
      expect(ZH[key], `ZH ${key}`).toBeTruthy();
    }
  });

  it("has no extra keys in the Chinese catalog", () => {
    expect(Object.keys(ZH).sort()).toEqual([...enKeys].sort());
  });

  it("leaves no English text in the Chinese catalog", () => {
    // Chinese strings may legitimately contain ASCII (key names, /workspace,
    // placeholders, product names), but should contain at least one Han character
    // unless the English source is itself symbol-only.
    const HAN = /[一-鿿]/;
    // Placeholder names are code, not copy: a string that is nothing but
    // `{title} · {status}` has no translatable text and must not be flagged.
    const copyOnly = (s: string) => s.replace(/\{\w+\}/g, "");
    for (const key of enKeys) {
      if (!/[A-Za-z]/.test(copyOnly(EN[key]))) continue;
      expect(
        HAN.test(ZH[key]),
        `ZH ${key} looks untranslated: ${ZH[key]}`,
      ).toBe(true);
    }
  });

  it("keeps the same placeholders on both sides", () => {
    const names = (s: string) => (s.match(/\{(\w+)\}/g) ?? []).sort();
    for (const key of enKeys) {
      expect(names(ZH[key]), `placeholders differ for ${key}`).toEqual(
        names(EN[key]),
      );
    }
  });
});

describe("format", () => {
  it("substitutes named placeholders", () => {
    expect(format("{count} steps", { count: 3 })).toBe("3 steps");
  });

  it("substitutes a placeholder used more than once", () => {
    expect(format("{a}-{a}", { a: "x" })).toBe("x-x");
  });

  it("leaves an unmatched placeholder verbatim so the bug is visible", () => {
    expect(format("{count} steps", {})).toBe("{count} steps");
  });

  it("returns the template unchanged with no params", () => {
    expect(format("plain")).toBe("plain");
  });
});

describe("translate", () => {
  it("reads the requested locale", () => {
    expect(translate("zh", "Project Files")).toBe("项目文件");
    expect(translate("en", "Project Files")).toBe("Project Files");
  });
});

describe("brand independence", () => {
  it("never hardcodes the default brand name in either catalog", () => {
    for (const key of Object.keys(EN) as MessageKey[]) {
      expect(EN[key], `EN ${key}`).not.toContain(defaultDisplayName);
      expect(ZH[key], `ZH ${key}`).not.toContain(defaultDisplayName);
    }
  });
});
