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
import { ZH as WORKSPACE } from "./messages-zh";

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
  const names = new Set(["CPU", "IPv4 CIDR", "TCP", "UDP", " BOM", "SHA-256", "Studio", "Volume Studio", "⌘P", "AGENTS.md: {size}"]);
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
    expect(translateText("zh", "update_attempt_unsettled: stale revision or conflicting intent")).toBe(COMMON_ZH.update_attempt_unsettled);
    expect(translateText("zh", "unregistered_code: engine diagnostic")).toBe("unregistered_code: engine diagnostic");
    expect(translateText("zh", "APIError: AGENTS.md: Add the required root UTF-8 text file.", undefined, STUDIO)).toBe("AGENTS.md：请添加必需的根目录 UTF-8 文本文件。");
    expect(translateText("zh", "skills/English Name/SKILL.md: Add the skill's SKILL.md file.", undefined, STUDIO)).toBe("skills/English Name/SKILL.md：请添加该技能的 SKILL.md 文件。");
  });
  it("translates every shared-assets validation finding supplied by the services", () => {
    for (const file of ["contracts/volumemanifest/shared_assets.go", "volumes-studio/backend/internal/authoring/validation.go"]) {
      const source = readFileSync(resolve(__dirname, "../../../", file), "utf8");
      for (const finding of source.matchAll(/(?:add\([^\n]*|Message:|Finding\{[^\n]*)"([^"]+\.)"/g))
        expect(STUDIO[finding[1]], `${file}: ${finding[1]}`).toBeTruthy();
    }
  });
  for (const [root, catalog] of [["frontend", WORKSPACE], ["admin-frontend", ADMIN], ["volumes-studio/frontend", STUDIO]] as const) {
    it(`${root} has every literal UI message in a catalog`, () => {
      const copy: Record<string, string> = { ...COMMON_ZH, ...catalog };
      for (const directory of ["components", "app"]) {
        const folder = resolve(__dirname, "../../../", root, directory);
        for (const file of readdirSync(folder, { recursive: true }).filter(file => String(file).endsWith(".tsx"))) {
          const path = resolve(folder, String(file));
          const source = ts.createSourceFile(path, readFileSync(path, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
          // Catch bypasses of the translator, including conditional copy. Keep
          // technical identifiers and example input values explicitly scoped.
          const literal = (node: ts.Node) => {
            if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node))
              expect(/[A-Za-z]{2}/.test(node.text), `${file}: untranslated ${node.text}`).toBe(false);
            else if (ts.isConditionalExpression(node)) {
              literal(node.whenTrue); literal(node.whenFalse);
            } else if (ts.isBinaryExpression(node) && [ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.AmpersandAmpersandToken].includes(node.operatorToken.kind)) literal(node.right);
          };
          const walk = (node: ts.Node) => {
            if (ts.isCallExpression(node) && ["tr", "t", "message"].includes(node.expression.getText(source)) && node.arguments[0] && ts.isStringLiteral(node.arguments[0]))
              expect(copy[node.arguments[0].text], `${file}: ${node.arguments[0].text}`).toBeTruthy();
            if (ts.isJsxText(node) && node.text.trim() !== "PDF")
              expect(/[A-Za-z]{2}/.test(node.text), `${file}: untranslated ${node.text.trim()}`).toBe(false);
            if (ts.isJsxExpression(node) && node.expression && !ts.isJsxAttribute(node.parent) && !(file === "workbench/markdown-view.tsx" && node.expression.getText(source) === 'language ?? "text"')) literal(node.expression);
            if (ts.isJsxAttribute(node) && ["aria-label", "title", "placeholder", "alt"].includes(node.name.getText(source)) && node.initializer && ts.isStringLiteral(node.initializer) && !/^[\d.:T+/-]+$/.test(node.initializer.text))
              expect(/[A-Za-z]{2}/.test(node.initializer.text), `${file}: untranslated ${node.initializer.text}`).toBe(false);
            // Shared pickers and dialogs translate their literal descriptors at
            // render time; catalog checks also cover those indirect call sites.
            if (ts.isJsxAttribute(node) && ["label", "submit"].includes(node.name.getText(source)) && node.initializer && ts.isStringLiteral(node.initializer))
              expect(copy[node.initializer.text], `${file}: ${node.initializer.text}`).toBeTruthy();
            if (file !== "layout.tsx" && ts.isPropertyAssignment(node) && ["label", "description", "submit"].includes(node.name.getText(source)) && ts.isStringLiteral(node.initializer) && !["Ctrl-C", "Ctrl-D"].includes(node.initializer.text))
              expect(copy[node.initializer.text], `${file}: ${node.initializer.text}`).toBeTruthy();
            ts.forEachChild(node, walk);
          };
          walk(source);
        }
      }
    });
  }
});
