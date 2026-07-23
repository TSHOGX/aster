import { isRecord } from "./guards";
import type { TraceDiff } from "./types";

// Pure helpers for turning pi tool-call frames into trace metadata. Extracted
// from the harness provider so they can be unit-tested in isolation; they hold
// no state and must stay free of store/React concerns.

export const TRACE_RESULT_MAX = 2000;

// Derive a short human label from a pi tool-call input object, e.g. the file
// path being read or the command being run. Falls back to null when nothing
// obviously label-worthy is present.
export function toolDetailFromInput(input: unknown): string | null {
  if (!isRecord(input)) return null;
  for (const key of ["path", "file_path", "command", "cmd", "pattern", "query", "url", "name"]) {
    const value = input[key];
    if (typeof value === "string" && value.trim()) {
      const trimmed = value.trim();
      return trimmed.length > 80 ? `${trimmed.slice(0, 79)}…` : trimmed;
    }
  }
  return null;
}

// pi emits `toolName`/`args` on tool lifecycle events; older frames used
// `name`/`input`. Read the modern shape first and fall back so labels populate
// regardless of which the backend sends.
export function readToolName(payload: Record<string, unknown>): string | null {
  const modern = payload.toolName;
  if (typeof modern === "string" && modern.trim()) return modern;
  const legacy = payload.name;
  return typeof legacy === "string" && legacy.trim() ? legacy : null;
}

export function readToolArgs(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const modern = payload.args;
  if (isRecord(modern)) return modern;
  const legacy = payload.input;
  return isRecord(legacy) ? legacy : undefined;
}

export function truncate(value: string, max: number): string {
  return value.length > max ? `${value.slice(0, max - 1)}…` : value;
}

export function stringifyResult(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

// Build a diff preview from a write/edit tool's arguments so the trace node can
// render the change inline. Returns undefined for non-mutating tools.
export function diffFromToolArgs(
  toolName: string | null,
  args: Record<string, unknown> | undefined
): TraceDiff | undefined {
  if (!toolName || !args) return undefined;
  const name = toolName.toLowerCase();
  const path =
    typeof args.path === "string"
      ? args.path
      : typeof args.file_path === "string"
      ? args.file_path
      : undefined;
  if (name === "write") {
    const content = typeof args.content === "string" ? args.content : undefined;
    if (content === undefined) return undefined;
    return { path, content };
  }
  if (name === "edit") {
    const rawEdits = Array.isArray(args.edits) ? args.edits : null;
    if (rawEdits) {
      const edits = rawEdits
        .filter(isRecord)
        .map((e) => ({
          oldText: typeof e.oldText === "string" ? e.oldText : typeof e.old_text === "string" ? e.old_text : "",
          newText: typeof e.newText === "string" ? e.newText : typeof e.new_text === "string" ? e.new_text : ""
        }));
      if (edits.length > 0) return { path, edits };
    }
    const oldText =
      typeof args.oldText === "string" ? args.oldText : typeof args.old_text === "string" ? args.old_text : null;
    const newText =
      typeof args.newText === "string" ? args.newText : typeof args.new_text === "string" ? args.new_text : null;
    if (oldText !== null || newText !== null) {
      return { path, edits: [{ oldText: oldText ?? "", newText: newText ?? "" }] };
    }
  }
  return undefined;
}
