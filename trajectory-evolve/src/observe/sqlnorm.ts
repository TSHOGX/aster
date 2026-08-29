// observe/sqlnorm.ts — deterministic SQL fingerprinting.
//
// This is the cheapest load-bearing piece of the whole pipeline. Stripping
// literals turns "the same question asked again with different dates" into one
// fingerprint, so "an agent keeps re-deriving this" becomes a GROUP BY instead
// of an LLM judgment. No model is involved and none should be: two queries that
// differ only in a WHERE constant are the same query by construction.

import { createHash } from "node:crypto";

/** What a query was really doing. The shape decides which table archetype (if
 *  any) could retire it, so it is classified structurally, not semantically. */
export type QueryShape =
  | "enum_probe" // "what values exist in this column"
  | "coverage_probe" // "is there data for this range / how much"
  | "schema_probe" // "what tables/columns exist"
  | "analysis" // real work: aggregates, joins, windows
  | "other";

export interface NormalizedQuery {
  norm: string;
  sha: string;
  shape: QueryShape;
  tables: string[];
  columns: string[];
  filters: string[];
}

const KEYWORDS = new Set([
  "select", "from", "where", "group", "by", "order", "limit", "having", "join",
  "left", "right", "inner", "outer", "on", "as", "and", "or", "not", "in", "is",
  "null", "distinct", "count", "min", "max", "avg", "sum", "case", "when",
  "then", "else", "end", "asc", "desc", "between", "like", "union", "all",
  "with", "date", "cast", "interval", "day", "hour", "describe", "show",
  "partitions", "tables", "columns", "information_schema", "desc",
]);

function stripComments(sql: string): string {
  return sql.replace(/--[^\n]*/g, " ").replace(/\/\*[\s\S]*?\*\//g, " ");
}

/** Replace every literal with `?`. Order matters: strings first, so a quoted
 *  date is not shredded into digits by the numeric pass. */
function stripLiterals(sql: string): string {
  return sql
    .replace(/'(?:[^']|'')*'/g, "?")
    .replace(/"(?:[^"]|"")*"/g, "?")
    .replace(/\b\d+(?:\.\d+)?\b/g, "?")
    .replace(/\bin\s*\((?:\s*\?\s*,)*\s*\?\s*\)/gi, "in (?)");
}

function ident(raw: string): string {
  return raw.replace(/[`"\[\]]/g, "").toLowerCase();
}

function extractTables(norm: string): string[] {
  const out = new Set<string>();
  for (const m of norm.matchAll(/\b(?:from|join)\s+([a-z_][\w.]*)/g)) {
    const t = ident(m[1]!);
    // `from (select ...)` and bare keywords are not tables.
    if (!KEYWORDS.has(t)) out.add(t);
  }
  return [...out].sort();
}

function extractColumns(norm: string): string[] {
  const out = new Set<string>();
  // Column-ish identifiers in the projection, GROUP BY, and comparisons. A real
  // parser would be better; for fingerprint aggregation this is enough, and it
  // fails toward "fewer columns" rather than inventing ones.
  const selectPart = /select\s+([\s\S]*?)\s+from\b/.exec(norm)?.[1] ?? "";
  const groupPart = /group\s+by\s+([\s\S]*?)(?:\border\b|\blimit\b|\bhaving\b|$)/.exec(norm)?.[1] ?? "";
  for (const chunk of [selectPart, groupPart]) {
    for (const m of chunk.matchAll(/[a-z_][\w.]*/g)) {
      const c = ident(m[0]);
      if (!KEYWORDS.has(c) && c !== "*") out.add(c);
    }
  }
  for (const m of norm.matchAll(/([a-z_][\w.]*)\s*(?:=|>=|<=|>|<|!=|like|between|in)\s*/g)) {
    const c = ident(m[1]!);
    if (!KEYWORDS.has(c)) out.add(c);
  }
  return [...out].sort();
}

function extractFilters(norm: string): string[] {
  const out = new Set<string>();
  for (const m of norm.matchAll(
    /([a-z_][\w.]*)\s*(=|>=|<=|>|<|!=|like|between|in)\s*\??/g
  )) {
    const c = ident(m[1]!);
    if (!KEYWORDS.has(c)) out.add(`${c} ${m[2]}`);
  }
  return [...out].sort();
}

function classify(norm: string): QueryShape {
  if (/\binformation_schema\b|\bshow\s+(tables|columns|partitions)\b|^describe\b|^desc\b/.test(norm))
    return "schema_probe";
  // "what values exist": DISTINCT on a column, or GROUP BY with only a count.
  const distinctOnly = /select\s+distinct\s+[a-z_][\w.]*(\s*,\s*[a-z_][\w.]*)*\s+from/.test(norm);
  const groupCountOnly =
    /group\s+by/.test(norm) &&
    /select\s+[\s\S]*?count\s*\(/.test(norm) &&
    !/\b(avg|sum|stddev|percentile|window|over)\s*\(/.test(norm);
  const hasTimeAggregate = /\b(min|max)\s*\(\s*[a-z_][\w.]*\s*\)/.test(norm);
  if (distinctOnly && !hasTimeAggregate) return "enum_probe";
  // "is there data": min/max of a timestamp, or count(*) bucketed by a date-ish
  // column — the classic "does this range exist" probe against a big table.
  if (hasTimeAggregate || (groupCountOnly && /\b(dt|day|date|ts|month|partition)\b/.test(norm)))
    return "coverage_probe";
  if (groupCountOnly) return "enum_probe";
  if (/\b(avg|sum|percentile|stddev|over)\s*\(|join\b/.test(norm)) return "analysis";
  return "other";
}

export function normalizeSQL(sql: string): NormalizedQuery {
  const norm = stripLiterals(stripComments(sql))
    .replace(/\s+/g, " ")
    .replace(/\s*([(),;])\s*/g, "$1 ")
    .trim()
    .toLowerCase()
    .replace(/;+$/, "");
  return {
    norm,
    sha: createHash("sha256").update(norm).digest("hex").slice(0, 16),
    shape: classify(norm),
    tables: extractTables(norm),
    columns: extractColumns(norm),
    filters: extractFilters(norm),
  };
}

/** Guardrail for any SQL an agent proposes as a table's source. The agent
 *  designs; code decides whether the design is even runnable. Rejects anything
 *  that is not a single read-only SELECT over a known table. */
export function assertReadOnlySelect(sql: string, knownTables: string[]): void {
  const body = stripComments(sql).trim().replace(/;+\s*$/, "");
  if (/;/.test(body)) throw new Error("source_sql must be a single statement");
  if (!/^\s*(select|with)\b/i.test(body)) throw new Error("source_sql must start with SELECT/WITH");
  const banned = /\b(insert|update|delete|drop|alter|create|truncate|attach|pragma|vacuum|replace|grant|revoke)\b/i;
  if (banned.test(body)) throw new Error("source_sql contains a write/DDL keyword");
  const used = extractTables(body.toLowerCase());
  const unknown = used.filter((t) => !knownTables.includes(t));
  if (unknown.length) throw new Error(`source_sql references unknown table(s): ${unknown.join(", ")}`);
}

/** Force a row cap onto a SELECT so a bad design can't scan the warehouse. */
export function withRowCap(sql: string, cap: number): string {
  const body = sql.trim().replace(/;+\s*$/, "");
  return /\blimit\s+\d+\s*$/i.test(body) ? body : `${body} LIMIT ${cap}`;
}
