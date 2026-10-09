/** Compact line-start lexer state for distant source jumps. Offsets are UTF-16 code units. */
export type SourceLexFormat = "xml" | "yaml";
export type TokenKind = "tag" | "string" | "comment" | "cdata" | "key" | "number";
export type Token = { from: number; to: number; kind: TokenKind };

const XML_TEXT = 0, XML_TAG = 1, XML_DOUBLE = 2, XML_SINGLE = 3, XML_COMMENT = 4, XML_CDATA = 5;
function xmlKind(state: number): TokenKind | null {
  return state === XML_TAG ? "tag" : state === XML_DOUBLE || state === XML_SINGLE ? "string"
    : state === XML_COMMENT ? "comment" : state === XML_CDATA ? "cdata" : null;
}
/** Scan one range, preserving comment, CDATA, tag and attribute quote state. */
export function scanXml(text: string, from: number, to: number, startState: number, emit?: (token: Token) => void): number {
  let state = startState, runKind: TokenKind | null = null, runFrom = from;
  for (let i = from; i < to; i++) {
    const c = text[i];
    let charKind = xmlKind(state);
    let nextState = state;
    if (state === XML_TEXT && c === "<") {
      nextState = text.startsWith("<!--", i) ? XML_COMMENT : text.startsWith("<![CDATA[", i) ? XML_CDATA : XML_TAG;
      charKind = xmlKind(nextState);
    } else if (state === XML_TAG) {
      if (c === '"') { nextState = XML_DOUBLE; charKind = "string"; }
      else if (c === "'") { nextState = XML_SINGLE; charKind = "string"; }
      else if (c === ">") nextState = XML_TEXT;
    } else if (state === XML_DOUBLE && c === '"') nextState = XML_TAG;
    else if (state === XML_SINGLE && c === "'") nextState = XML_TAG;
    else if (state === XML_COMMENT && c === ">" && text[i - 1] === "-" && text[i - 2] === "-") nextState = XML_TEXT;
    else if (state === XML_CDATA && c === ">" && text[i - 1] === "]" && text[i - 2] === "]") nextState = XML_TEXT;
    if (charKind !== runKind) {
      if (runKind && i > runFrom) emit?.({ from: runFrom, to: i, kind: runKind });
      runFrom = i;
      runKind = charKind;
    }
    state = nextState;
  }
  if (runKind && to > runFrom) emit?.({ from: runFrom, to, kind: runKind });
  return state;
}

function lineBounds(text: string, start: number): [number, number] {
  const end = text.indexOf("\n", start);
  return [start, end < 0 ? text.length : end];
}
export function indexSourceLines(text: string, format: SourceLexFormat): Uint16Array {
  let lines = 1;
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) lines++;
  const states = new Uint16Array(lines);
  let state = 0, start = 0, line = 0;
  while (start <= text.length) {
    const [, end] = lineBounds(text, start);
    states[line++] = state;
    state = format === "xml" ? scanXml(text, start, end, state) : nextYamlBlockState(text, start, end, state);
    if (end === text.length) break;
    start = end + 1;
  }
  return states;
}

/** A YAML block scalar continues over blank lines and lines indented past its marker. */
export function nextYamlBlockState(text: string, from: number, to: number, prior: number): number {
  const line = text.slice(from, to).replace(/\r$/, "");
  if (!line.trim()) return prior;
  const indent = /^\s*/.exec(line)?.[0].length ?? 0;
  const active = prior > 0 && indent > prior - 1;
  if (active) return prior;
  return /:\s*[|>][-+0-9]*\s*(?:#.*)?$/.test(line) ? Math.min(indent + 1, 65535) : 0;
}

export function yamlTokens(text: string, from: number, to: number, prior: number): Token[] {
  const line = text.slice(from, to).replace(/\r$/, "");
  const indent = /^\s*/.exec(line)?.[0].length ?? 0;
  if (prior > 0 && (!line.trim() || indent > prior - 1))
    return line.length ? [{ from, to: from + line.length, kind: "string" }] : [];
  const tokens: Token[] = [];
  const key = /^(\s*(?:-\s*)?)([^:#\s][^:]*?)(\s*:)/.exec(line);
  if (key) tokens.push({ from: from + key[1].length, to: from + key[1].length + key[2].length, kind: "key" });
  let quote: "'" | '"' | null = null, quoteFrom = 0;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote) {
      if (c === quote && (quote === "'" || line[i - 1] !== "\\")) {
        tokens.push({ from: from + quoteFrom, to: from + i + 1, kind: "string" });
        quote = null;
      }
    } else if (c === "#") {
      tokens.push({ from: from + i, to: from + line.length, kind: "comment" });
      break;
    } else if (c === '"' || c === "'") { quote = c; quoteFrom = i; }
  }
  if (quote) tokens.push({ from: from + quoteFrom, to: from + line.length, kind: "string" });
  return tokens;
}
