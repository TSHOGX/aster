import { describe, expect, it } from "vitest";

import { codeMirrorLang, hljsLanguage } from "./languages";

describe("hljsLanguage", () => {
  // Locks the former lib/mime.ts CODE_EXT map verbatim.
  const cases: Array<[string, string]> = [
    ["js", "javascript"],
    ["jsx", "javascript"],
    ["ts", "typescript"],
    ["tsx", "typescript"],
    ["py", "python"],
    ["go", "go"],
    ["rs", "rust"],
    ["java", "java"],
    ["rb", "ruby"],
    ["php", "php"],
    ["sh", "bash"],
    ["bash", "bash"],
    ["zsh", "bash"],
    ["c", "c"],
    ["h", "c"],
    ["cpp", "cpp"],
    ["hpp", "cpp"],
    ["cs", "csharp"],
    ["swift", "swift"],
    ["kt", "kotlin"],
    ["css", "css"],
    ["scss", "scss"],
    ["html", "xml"],
    ["xml", "xml"],
    ["sql", "sql"],
    ["yaml", "yaml"],
    ["yml", "yaml"],
    ["toml", "ini"],
    ["ini", "ini"]
  ];
  it.each(cases)("maps %s -> %s", (ext, id) => {
    expect(hljsLanguage(ext)).toBe(id);
  });
  it("returns undefined for extensions never treated as highlightable code", () => {
    for (const ext of ["json", "md", "mjs", "cjs", "htm", "txt", "png"]) {
      expect(hljsLanguage(ext)).toBeUndefined();
    }
  });
});

describe("codeMirrorLang", () => {
  // Locks the former code-editor.tsx languageForPath switch verbatim.
  const cases: Array<[string, string]> = [
    ["js", "javascript"],
    ["jsx", "javascript"],
    ["mjs", "javascript"],
    ["cjs", "javascript"],
    ["ts", "typescript"],
    ["tsx", "tsx"],
    ["json", "json"],
    ["py", "python"],
    ["md", "markdown"],
    ["markdown", "markdown"],
    ["mdx", "markdown"],
    ["html", "html"],
    ["htm", "html"],
    ["css", "css"],
    ["scss", "css"]
  ];
  it.each(cases)("maps %s -> %s", (ext, key) => {
    expect(codeMirrorLang(ext)).toBe(key);
  });
  it("returns undefined for plain-text extensions", () => {
    for (const ext of ["go", "rs", "txt", "sql", "yaml"]) {
      expect(codeMirrorLang(ext)).toBeUndefined();
    }
  });
});
