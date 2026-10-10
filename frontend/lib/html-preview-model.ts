import { Parser } from "htmlparser2";

export type HtmlBlock = { kind: "heading" | "paragraph" | "row"; level?: number; text: string };
export type HtmlPreviewModel = {
  nodes: number;
  scripts: number;
  blocks: HtmlBlock[];
  omittedBlocks: number;
  simplified: boolean;
};

const NODE_BUDGET = 30_000;
const BLOCK_BUDGET = 2_000;
const BLOCK_CHARS = 2_000;
const ignored = new Set(["script", "style", "noscript", "svg"]);
const blockKind = (name: string): HtmlBlock["kind"] | null =>
  /^h[1-6]$/.test(name) ? "heading"
    : name === "tr" ? "row"
      : ["p", "li", "figcaption", "blockquote", "pre", "summary"].includes(name) ? "paragraph" : null;

/** Extracts bounded semantic blocks without constructing an HTML DOM or running scripts. */
export function analyzeHTML(source: string): HtmlPreviewModel {
  let nodes = 0, scripts = 0, omittedBlocks = 0, ignoredDepth = 0;
  const blocks: HtmlBlock[] = [];
  let active: { tag: string; kind: HtmlBlock["kind"]; level?: number; text: string } | null = null;
  const push = (block: HtmlBlock) => {
    if (blocks.length < BLOCK_BUDGET) blocks.push(block);
    else omittedBlocks++;
  };
  const flush = () => {
    if (!active) return;
    const text = active.text.replace(/[ \t]+/g, " ").replace(/\s*\n\s*/g, "\n").trim();
    if (text) {
      push({ kind: active.kind, ...(active.level ? { level: active.level } : {}), text });
    }
    active = null;
  };
  const parser = new Parser({
    onopentag(name) {
      nodes++;
      if (name === "script") scripts++;
      if (ignored.has(name)) { ignoredDepth++; return; }
      if (ignoredDepth) return;
      if (name === "br" && active) active.text += "\n";
      const kind = blockKind(name);
      if (kind) {
        flush();
        active = { tag: name, kind, ...(kind === "heading" ? { level: Number(name[1]) } : {}), text: "" };
      }
    },
    ontext(text) {
      if (ignoredDepth) return;
      if (!active) {
        const value = text.trim();
        if (value) push({ kind: "paragraph", text: value.slice(0, BLOCK_CHARS) });
      } else if (active.text.length < BLOCK_CHARS)
        active.text += text.slice(0, BLOCK_CHARS - active.text.length);
    },
    onclosetag(name) {
      if (ignored.has(name)) { ignoredDepth = Math.max(0, ignoredDepth - 1); return; }
      if (ignoredDepth) return;
      if ((name === "td" || name === "th") && active?.kind === "row") active.text += " · ";
      if (active?.tag === name) flush();
    },
    onend: flush,
  }, { decodeEntities: true });
  parser.end(source);
  return { nodes, scripts, blocks, omittedBlocks, simplified: nodes > NODE_BUDGET };
}
