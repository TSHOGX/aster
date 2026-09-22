import { expect, it } from "vitest";
import { MARKDOWN_LIMITS, richMarkdownAllowed } from "./markdown-budget";

it("retains ordinary Markdown and counts actual UTF-8 bytes", () => {
  expect(
    richMarkdownAllowed(
      "# Report\n\n**Result** 中文 [reference](https://example.com)\n\n```python\nprint(1)\n```\n",
    ),
  ).toBe(true);
  expect(richMarkdownAllowed("x".repeat(MARKDOWN_LIMITS.bytes))).toBe(true);
  expect(richMarkdownAllowed("x".repeat(MARKDOWN_LIMITS.bytes + 1))).toBe(
    false,
  );
  expect(richMarkdownAllowed("中".repeat(2730))).toBe(true);
  expect(richMarkdownAllowed("中".repeat(2731))).toBe(false);
  expect(richMarkdownAllowed("🔋".repeat(2048))).toBe(true);
  expect(richMarkdownAllowed("🔋".repeat(2049))).toBe(false);
});
it("rejects dense syntax and deep delimiter input before parsing", () => {
  for (const content of [
    "[".repeat(1000),
    "> ".repeat(17) + "deep",
    "| x ".repeat(2049),
    "*x* ".repeat(2049),
    "x\n".repeat(2048),
  ])
    expect(richMarkdownAllowed(content)).toBe(false);
  expect(richMarkdownAllowed("Long line without markup. ".repeat(100))).toBe(
    true,
  );
});
