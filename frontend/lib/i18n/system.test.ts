import { afterEach, describe, expect, it, vi } from "vitest";
import { runInNewContext } from "node:vm";
import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import ts from "typescript";
import { currentLocale, localeInitScript, message, setLanguage, validLanguage } from "../../../web/i18n/locale";
import { translateText } from "../../../web/i18n/text";
import { COMMON_ZH } from "../../../web/i18n/messages";
import { cachedAppearance } from "../../../web/appearance";
import { ZH as ADMIN } from "../../../admin-frontend/lib/i18n/messages";
import { ZH as STUDIO } from "../../../volumes-studio/frontend/lib/i18n/messages";

afterEach(() => { setLanguage("system"); vi.unstubAllGlobals(); });

describe("account language", () => {
  it("uses an explicit preference before the browser and resets to browser on logout", () => {
    vi.stubGlobal("navigator", { languages: ["zh-CN", "en"] });
    expect(currentLocale()).toBe("zh");
    setLanguage("en"); expect(currentLocale()).toBe("en");
    setLanguage("system"); expect(currentLocale()).toBe("zh");
    expect(validLanguage("zh-CN")).toBe(false);
    expect(validLanguage("system")).toBe(true);
  });
  it("normalizes older caches but rejects invalid or another account's cache", () => {
    let cached = { account_id: "a", revision: 2, theme_mode: "dark", accent: "neutral" };
    vi.stubGlobal("localStorage", { getItem: () => JSON.stringify(cached) });
    expect(cachedAppearance("a")?.language).toBe("system");
    expect(cachedAppearance("b")).toBeNull();
    cached = { ...cached, language: "zh-CN" } as typeof cached;
    expect(cachedAppearance("a")).toBeNull();
  });
  it("only uses a cache for an identified account before paint", () => {
    const run = (accountId?: string, pathname = "/admin/") => {
      const html = { dataset: { accountId }, lang: "en" };
      runInNewContext(localeInitScript, {
        navigator: { languages: ["en-US"] }, document: { documentElement: html }, location: { pathname },
        localStorage: { getItem: () => JSON.stringify({ account_id: "a", revision: 1, theme_mode: "dark", accent: "neutral", language: "zh" }) },
      });
      return html.lang;
    };
    expect(run("a")).toBe("zh-Hans");
    expect(run("b")).toBe("en");
    expect(run()).toBe("en");
    expect(run("a", "/s/public-token")).toBe("en");
  });
});

describe("Admin and Studio copy", () => {
  const names = new Set(["CPU", "IPv4 CIDR", "TCP", "UDP", " BOM", "SHA-256", "Studio", "Volume Studio", "⌘P"]);
  for (const [name, catalog] of Object.entries({ common: COMMON_ZH, admin: ADMIN, studio: STUDIO })) {
    it(`${name} catalog translates copy and preserves placeholders`, () => {
      for (const [key, value] of Object.entries(catalog)) {
        expect(value, key).toBeTruthy();
        expect((value.match(/\{\w+\}/g) || []).sort(), key).toEqual((key.match(/\{\w+\}/g) || []).sort());
        if (/[a-z]/i.test(value.replace(/\{\w+\}/g, "")) && !names.has(key)) expect(/[一-鿿]/.test(value), key).toBe(true);
      }
    });
  }
  it("translates stored confirmation descriptors without translating user parameters", () => {
    const prompt = message("Save changes to {path}?", { path: "English Name.md" });
    expect(translateText("zh", prompt)).toBe("保存对 English Name.md 的更改？");
    expect(translateText("en", prompt)).toBe("Save changes to English Name.md?");
    expect(translateText("zh", "opaque engine diagnostic")).toBe("opaque engine diagnostic");
  });
  it("preserves diagnostics and placeholders that match inherited object properties", () => {
    for (const key of ["constructor", "toString", "__proto__"])
      expect(translateText("zh", key)).toBe(key);
    expect(translateText("en", "{constructor}", {})).toBe("{constructor}");
  });
  for (const [root, catalog] of [["admin-frontend", ADMIN], ["volumes-studio/frontend", STUDIO]] as const) {
    it(`${root} has every literal UI message in a catalog`, () => {
      const copy = { ...COMMON_ZH, ...catalog };
      for (const directory of ["components", "app"]) {
        const folder = resolve(__dirname, "../../../", root, directory);
        for (const file of readdirSync(folder, { recursive: true }).filter(file => String(file).endsWith(".tsx"))) {
          const path = resolve(folder, String(file));
          const source = ts.createSourceFile(path, readFileSync(path, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
          const walk = (node: ts.Node) => {
            if (ts.isCallExpression(node) && ["tr", "message"].includes(node.expression.getText(source)) && node.arguments[0] && ts.isStringLiteral(node.arguments[0]))
              expect(copy[node.arguments[0].text], `${file}: ${node.arguments[0].text}`).toBeTruthy();
            ts.forEachChild(node, walk);
          };
          walk(source);
        }
      }
    });
  }
});
