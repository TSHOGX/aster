import { describe, expect, it } from "vitest";

import { formatDuration, stripMarkdown } from "./format";

describe("formatDuration", () => {
  it("renders sub-second values in milliseconds", () => {
    expect(formatDuration(0)).toBe("0ms");
    expect(formatDuration(820)).toBe("820ms");
    expect(formatDuration(999)).toBe("999ms");
  });

  it("renders seconds with one decimal below a minute", () => {
    expect(formatDuration(1000)).toBe("1.0s");
    expect(formatDuration(3200)).toBe("3.2s");
    expect(formatDuration(59_900)).toBe("59.9s");
  });

  it("renders minutes and zero-padded seconds at/above a minute", () => {
    expect(formatDuration(60_000)).toBe("1m 00s");
    expect(formatDuration(65_000)).toBe("1m 05s");
    expect(formatDuration(125_000)).toBe("2m 05s");
  });

  it("returns empty string for invalid input", () => {
    expect(formatDuration(null)).toBe("");
    expect(formatDuration(undefined)).toBe("");
    expect(formatDuration(-1)).toBe("");
    expect(formatDuration(Number.NaN)).toBe("");
  });
});

describe("stripMarkdown", () => {
  it("strips headings, emphasis and inline code", () => {
    expect(stripMarkdown("# Title")).toBe("Title");
    expect(stripMarkdown("**bold** and *italic*")).toBe("bold and italic");
    expect(stripMarkdown("use `npm run test` now")).toBe("use npm run test now");
  });

  it("keeps link text and drops the url", () => {
    expect(stripMarkdown("see [the docs](https://example.com)")).toBe("see the docs");
    expect(stripMarkdown("![alt](img.png)")).toBe("alt");
  });

  it("strips blockquote and list markers", () => {
    expect(stripMarkdown("> quoted")).toBe("quoted");
    expect(stripMarkdown("- one\n- two")).toBe("one\ntwo");
  });

  it("unwraps fenced code blocks to their contents", () => {
    expect(stripMarkdown("```js\nconst x = 1;\n```")).toBe("const x = 1;");
  });
});
