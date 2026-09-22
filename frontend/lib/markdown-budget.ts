/** Conservative pre-parser limits: a fixed linear scan avoids constructing an
 * expensive Markdown tree just to decide that it exceeds the working budget. */
export const MARKDOWN_LIMITS = Object.freeze({
  bytes: 8 * 1024,
  lines: 1024,
  syntax: 2048,
  tableSeparators: 1024,
  delimiterRun: 32,
  quoteDepth: 16,
});
export function richMarkdownAllowed(content: string): boolean {
  if (content.length > MARKDOWN_LIMITS.bytes) return false;
  let bytes = 0,
    lines = 1,
    syntax = 0,
    pipes = 0,
    run = 0,
    quotes = 0;
  for (let i = 0; i < content.length; i++) {
    const code = content.charCodeAt(i),
      char = content[i];
    if (code < 0x80) bytes++;
    else if (code < 0x800) bytes += 2;
    else if (
      code >= 0xd800 &&
      code <= 0xdbff &&
      content.charCodeAt(i + 1) >= 0xdc00 &&
      content.charCodeAt(i + 1) <= 0xdfff
    ) {
      bytes += 4;
      i++;
    } else bytes += 3;
    if (bytes > MARKDOWN_LIMITS.bytes) return false;
    if (char === "\n") {
      if (++lines > MARKDOWN_LIMITS.lines) return false;
      quotes = 0;
    }
    if (char === ">" && ++quotes > MARKDOWN_LIMITS.quoteDepth) return false;
    if (char === "|" && ++pipes > MARKDOWN_LIMITS.tableSeparators) return false;
    if ("*_~`[]()<>|#!".includes(char)) {
      if (++syntax > MARKDOWN_LIMITS.syntax) return false;
    }
    if ("*_~`[]()<>".includes(char)) {
      if (++run > MARKDOWN_LIMITS.delimiterRun) return false;
    } else run = 0;
  }
  return true;
}
