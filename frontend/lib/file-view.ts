import { classifyArtifact } from "./mime";

import { PREVIEW_LIMITS } from "./preview-budget";

export const TEXT_FILE_LIMIT = PREVIEW_LIMITS.blobBytes;
export const TEXT_EDIT_LIMIT = 1 << 20;
export const FORMATTED_TEXT_LIMIT = 256 * 1024;
export function isTextFile(path: string) {
  return ["code", "text", "markdown", "html", "json", "table"].includes(classifyArtifact(path).kind);
}
export function hasSourceView(path: string) {
  return ["markdown", "html", "table", "json"].includes(classifyArtifact(path).kind);
}
export async function decodeTextFile(path: string, blob: Blob): Promise<string | null> {
  if (!isTextFile(path) || blob.size > TEXT_FILE_LIMIT) return null;
  try {
    const bytes = new Uint8Array(await blob.arrayBuffer());
    const encoding = bytes[0] === 0xff && bytes[1] === 0xfe ? "utf-16le"
      : bytes[0] === 0xfe && bytes[1] === 0xff ? "utf-16be" : "utf-8";
    const text = new TextDecoder(encoding, {fatal:true}).decode(bytes);
    return text.includes("\0") ? null : text;
  } catch { return null; }
}

/** Bounded RFC 4180-style CSV/TSV decoding; cells remain plain React text. */
export function parseDelimited(text: string, delimiter: "," | "\t") {
  const rows: string[][] = [];
  let row: string[] = [], cell = "", quoted = false, closed = false, cells = 0;
  const pushCell = () => { row.push(cell); cell = ""; closed = false; cells++; };
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') { cell += '"'; i++; }
        else { quoted = false; closed = true; }
      } else cell += c;
    } else if (c === delimiter || c === "\n" || c === "\r") {
      pushCell();
      if (c !== delimiter) {
        rows.push(row); row = [];
        if (c === "\r" && text[i + 1] === "\n") i++;
        if (rows.length >= 2000 || cells >= 20000)
          return {rows, truncated:i + 1 < text.length};
      } else if (row.length >= 256 || cells >= 20000) {
        rows.push(row);
        return {rows, truncated:true};
      }
    } else if (c === '"' && !cell && !closed) quoted = true;
    else if (closed) throw new Error("Invalid delimited text");
    else cell += c;
  }
  if (quoted) throw new Error("Unclosed quoted field");
  if (cell || row.length || closed) { pushCell(); rows.push(row); }
  return {rows, truncated:false};
}
