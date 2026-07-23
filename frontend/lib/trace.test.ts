import { describe, expect, it } from "vitest";

import {
  diffFromToolArgs,
  readToolArgs,
  readToolName,
  stringifyResult,
  toolDetailFromInput,
  truncate
} from "./trace";

describe("readToolName", () => {
  it("prefers the modern toolName field", () => {
    expect(readToolName({ toolName: "bash", name: "legacy" })).toBe("bash");
  });
  it("falls back to the legacy name field", () => {
    expect(readToolName({ name: "read" })).toBe("read");
  });
  it("returns null when neither is a non-empty string", () => {
    expect(readToolName({ toolName: "  ", name: 42 })).toBeNull();
  });
});

describe("readToolArgs", () => {
  it("prefers modern args over legacy input", () => {
    expect(readToolArgs({ args: { a: 1 }, input: { b: 2 } })).toEqual({ a: 1 });
  });
  it("falls back to input when args is absent", () => {
    expect(readToolArgs({ input: { b: 2 } })).toEqual({ b: 2 });
  });
  it("returns undefined when neither is a record", () => {
    expect(readToolArgs({ args: "x" })).toBeUndefined();
  });
});

describe("toolDetailFromInput", () => {
  it("extracts the first label-worthy string field", () => {
    expect(toolDetailFromInput({ file_path: "src/index.ts" })).toBe("src/index.ts");
  });
  it("truncates long values to 80 chars with an ellipsis", () => {
    const detail = toolDetailFromInput({ command: "a".repeat(200) });
    expect(detail).toHaveLength(80);
    expect(detail?.endsWith("…")).toBe(true);
  });
  it("returns null for non-record input", () => {
    expect(toolDetailFromInput(null)).toBeNull();
  });
});

describe("truncate", () => {
  it("leaves short strings unchanged", () => {
    expect(truncate("hello", 10)).toBe("hello");
  });
  it("clips and appends an ellipsis past the max", () => {
    expect(truncate("abcdef", 4)).toBe("abc…");
  });
});

describe("stringifyResult", () => {
  it("returns strings verbatim", () => {
    expect(stringifyResult("done")).toBe("done");
  });
  it("returns an empty string for null/undefined", () => {
    expect(stringifyResult(null)).toBe("");
    expect(stringifyResult(undefined)).toBe("");
  });
  it("pretty-prints objects", () => {
    expect(stringifyResult({ a: 1 })).toBe("{\n  \"a\": 1\n}");
  });
});

describe("diffFromToolArgs", () => {
  it("builds a write diff with path and content", () => {
    expect(diffFromToolArgs("Write", { path: "a.txt", content: "hi" })).toEqual({
      path: "a.txt",
      content: "hi"
    });
  });
  it("builds an edit diff from an edits array, normalizing snake_case", () => {
    expect(
      diffFromToolArgs("edit", { file_path: "a.txt", edits: [{ old_text: "x", new_text: "y" }] })
    ).toEqual({ path: "a.txt", edits: [{ oldText: "x", newText: "y" }] });
  });
  it("builds an edit diff from flat oldText/newText", () => {
    expect(diffFromToolArgs("edit", { oldText: "x", newText: "y" })).toEqual({
      path: undefined,
      edits: [{ oldText: "x", newText: "y" }]
    });
  });
  it("returns undefined for non-mutating tools", () => {
    expect(diffFromToolArgs("bash", { command: "ls" })).toBeUndefined();
  });
});
