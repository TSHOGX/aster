/**
 * Sandbox-side landing for MCP query results.
 *
 * Pi-facing executeQuery grows an optional `output` argument
 * (`file:///workspace/data/x.csv` or `/workspace/data/x.csv`). The bridge
 * strips it before the upstream call, writes CSV under the workspace, and
 * returns a short summary so the model never sees the row payload.
 *
 * Oversized replies are auto-landed the same way even when `output` is omitted.
 */

import * as fs from "node:fs";
import * as path from "node:path";

export const FILE_OUTPUT_PARAM = "output";
export const DEFAULT_WORKSPACE_ROOT = "/workspace";
export const DEFAULT_AUTO_LAND_MAX_CHARS = 4096;
export const DEFAULT_AUTO_LAND_MAX_ROWS = 20;
export const DEFAULT_SAMPLE_ROWS = 3;
export const SAMPLE_CELL_MAX_CHARS = 200;

export type PiTextContent = { type: "text"; text: string };

export class QueryOutputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "QueryOutputError";
  }
}

export function isExecuteQueryTool(wireName: string): boolean {
  return wireName === "executeQuery";
}

/** Inject optional `output` into an MCP JSON Schema object. */
export function withFileOutputSchema(
  inputSchema: Record<string, unknown>,
): Record<string, unknown> {
  const schema =
    inputSchema && typeof inputSchema === "object" && !Array.isArray(inputSchema)
      ? structuredClone(inputSchema)
      : { type: "object" };
  if (schema["type"] && schema["type"] !== "object") {
    return schema;
  }
  schema["type"] = "object";
  const props =
    schema["properties"] &&
    typeof schema["properties"] === "object" &&
    !Array.isArray(schema["properties"])
      ? (schema["properties"] as Record<string, unknown>)
      : {};
  schema["properties"] = props;
  if (!props[FILE_OUTPUT_PARAM]) {
    props[FILE_OUTPUT_PARAM] = {
      type: "string",
      description:
        'Write the result to this workspace CSV instead of returning rows to the model. Use "file:///workspace/data/<name>.csv" or "/workspace/data/<name>.csv". The tool result is path, rowCount, columns, and up to 3 sample rows.',
    };
  }
  return schema;
}

export function stripOutputParam(params: unknown): {
  arguments: Record<string, unknown>;
  output: string | undefined;
} {
  if (!params || typeof params !== "object" || Array.isArray(params)) {
    return { arguments: {}, output: undefined };
  }
  const record = { ...(params as Record<string, unknown>) };
  const raw = record[FILE_OUTPUT_PARAM];
  delete record[FILE_OUTPUT_PARAM];
  const output =
    typeof raw === "string" && raw.trim().length > 0 ? raw.trim() : undefined;
  return { arguments: record, output };
}

export function resolveWorkspaceOutputPath(
  raw: string,
  workspaceRoot: string,
): string {
  let input = raw.trim();
  if (!input) {
    throw new QueryOutputError("output path is empty");
  }
  if (input.startsWith("file:")) {
    let parsed: URL;
    try {
      parsed = new URL(input);
    } catch {
      throw new QueryOutputError(`invalid file URL: ${raw}`);
    }
    if (parsed.protocol !== "file:") {
      throw new QueryOutputError("output must be a file:// URL or a /workspace path");
    }
    const host = parsed.hostname;
    if (host && host !== "localhost" && host !== "127.0.0.1") {
      throw new QueryOutputError(`output file URL must be local, got host ${host}`);
    }
    input = decodeURIComponent(parsed.pathname);
  }
  if (!path.isAbsolute(input)) {
    input = path.join(workspaceRoot, input);
  }
  const root = path.resolve(workspaceRoot);
  const resolved = path.resolve(input);
  const rel = path.relative(root, resolved);
  if (rel.startsWith("..") || path.isAbsolute(rel)) {
    throw new QueryOutputError(`output must stay under ${root}`);
  }
  if (resolved === root) {
    throw new QueryOutputError(
      "output must be a file under the workspace, not the workspace root",
    );
  }
  return resolved;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

/** Pull a tabular row list out of common MCP JSON shapes. */
export function extractRows(payload: unknown): Record<string, unknown>[] | null {
  if (Array.isArray(payload)) {
    if (payload.length === 0) return [];
    if (payload.every(isPlainObject)) return payload as Record<string, unknown>[];
    return payload.map((value, index) => ({ index, value }));
  }
  if (!isPlainObject(payload)) return null;
  for (const key of ["data", "rows", "records", "result", "items"]) {
    if (!(key in payload)) continue;
    const inner = payload[key];
    if (typeof inner === "string") {
      try {
        const nested = extractRows(JSON.parse(inner));
        if (nested) return nested;
      } catch {
        continue;
      }
    } else {
      const nested = extractRows(inner);
      if (nested) return nested;
    }
  }
  return null;
}

export function csvEscape(value: unknown): string {
  if (value == null) return "";
  const text =
    typeof value === "string"
      ? value
      : typeof value === "object"
        ? JSON.stringify(value)
        : String(value);
  if (/[",\r\n]/.test(text)) return `"${text.replace(/"/g, '""')}"`;
  return text;
}

export function rowsToCsv(rows: Record<string, unknown>[]): string {
  const cols: string[] = [];
  const seen = new Set<string>();
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!seen.has(key)) {
        seen.add(key);
        cols.push(key);
      }
    }
  }
  const lines = [cols.map(csvEscape).join(",")];
  for (const row of rows) {
    lines.push(cols.map((col) => csvEscape(row[col])).join(","));
  }
  return lines.join("\n") + (lines.length > 0 ? "\n" : "");
}

function truncateCell(value: unknown): unknown {
  if (typeof value !== "string" || value.length <= SAMPLE_CELL_MAX_CHARS) {
    return value;
  }
  return value.slice(0, SAMPLE_CELL_MAX_CHARS) + "…";
}

function parseJoinedPayload(text: string): unknown | undefined {
  const trimmed = text.trim();
  if (!trimmed) return undefined;
  try {
    return JSON.parse(trimmed);
  } catch {
    const startObj = trimmed.indexOf("{");
    const startArr = trimmed.indexOf("[");
    let start = -1;
    if (startObj >= 0 && (startArr < 0 || startObj < startArr)) start = startObj;
    else if (startArr >= 0) start = startArr;
    if (start < 0) return undefined;
    const endObj = trimmed.lastIndexOf("}");
    const endArr = trimmed.lastIndexOf("]");
    const end = Math.max(endObj, endArr);
    if (end <= start) return undefined;
    try {
      return JSON.parse(trimmed.slice(start, end + 1));
    } catch {
      return undefined;
    }
  }
}

export interface QueryOutputOptions {
  workspaceRoot: string;
  output?: string;
  autoLandMaxChars?: number;
  autoLandMaxRows?: number;
  sampleRows?: number;
  now?: Date;
  mkdir?: typeof fs.mkdirSync;
  writeFile?: typeof fs.writeFileSync;
}

export interface QueryOutputResult {
  content: PiTextContent[];
  landed: boolean;
  path?: string;
  auto?: boolean;
}

function defaultAutoPath(workspaceRoot: string, now: Date): string {
  const stamp = now.toISOString().replace(/[-:.]/g, "");
  return path.join(workspaceRoot, "data", `query_${stamp}.csv`);
}

export function applyQueryOutput(
  content: PiTextContent[],
  opts: QueryOutputOptions,
): QueryOutputResult {
  const text = content.map((c) => c.text).join("\n");
  const payload = parseJoinedPayload(text);
  const rows = payload === undefined ? null : extractRows(payload);
  const rowCount = rows ? rows.length : 0;
  const autoLandMaxChars = opts.autoLandMaxChars ?? DEFAULT_AUTO_LAND_MAX_CHARS;
  const autoLandMaxRows = opts.autoLandMaxRows ?? DEFAULT_AUTO_LAND_MAX_ROWS;
  const sampleN = opts.sampleRows ?? DEFAULT_SAMPLE_ROWS;
  const now = opts.now ?? new Date();

  const explicit = Boolean(opts.output);
  const oversized =
    text.length > autoLandMaxChars || (rows !== null && rowCount > autoLandMaxRows);
  if (!explicit && !oversized) {
    return { content, landed: false };
  }

  const dest = resolveWorkspaceOutputPath(
    opts.output ?? defaultAutoPath(opts.workspaceRoot, now),
    opts.workspaceRoot,
  );
  const mkdir = opts.mkdir ?? fs.mkdirSync;
  const writeFile = opts.writeFile ?? fs.writeFileSync;
  mkdir(path.dirname(dest), { recursive: true });

  let format: "csv" | "text" = "text";
  let cols: string[] = [];
  let body = text;
  if (rows) {
    format = "csv";
    const seen = new Set<string>();
    for (const row of rows) {
      for (const key of Object.keys(row)) {
        if (!seen.has(key)) {
          seen.add(key);
          cols.push(key);
        }
      }
    }
    body = rowsToCsv(rows);
  }
  writeFile(dest, body, "utf8");

  const sample = rows
    ? rows.slice(0, sampleN).map((row) => {
        const out: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(row)) out[k] = truncateCell(v);
        return out;
      })
    : [];

  const summary = {
    landed: true,
    auto: !explicit,
    path: dest,
    format,
    rowCount: rows ? rowCount : undefined,
    cols: cols.length > 0 ? cols : undefined,
    sample: sample.length > 0 ? sample : undefined,
    bytes: Buffer.byteLength(body, "utf8"),
  };

  return {
    content: [{ type: "text", text: JSON.stringify(summary) }],
    landed: true,
    path: dest,
    auto: !explicit,
  };
}
