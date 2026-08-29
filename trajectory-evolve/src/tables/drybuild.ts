// tables/drybuild.ts — the deterministic half of the table track.
//
// The boundary this file draws is the important part: an agent proposes a schema
// and a SELECT; CODE decides whether that proposal is even runnable, runs it
// against a read-only source, and refuses to ship the result unless it passes a
// health check. There is no path by which an agent's output becomes a published
// artifact without clearing these gates, and no path by which it writes to the
// warehouse at all.

import { mkdirSync, writeFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { db, nowISO } from "../db.ts";
import { files, limits } from "../config.ts";
import { openWarehouse, warehouseSchema } from "../warehouse.ts";
import { assertReadOnlySelect, withRowCap } from "../observe/sqlnorm.ts";

export interface TableDesign {
  name: string;
  domain: string;
  archetype: string;
  intent: string;
  source: "warehouse" | "patterns";
  columns: Array<{ name: string; type: string; desc: string }>;
  source_sql: string;
  grain: string;
  refresh_cron: string;
  caveats_md: string;
}

export interface BuildHealth {
  rows: number;
  emptyColumns: string[];
  duplicateKeys: number;
  overCap: boolean;
  ok: boolean;
  problems: string[];
}

export interface BuildResult {
  ok: boolean;
  rows: number;
  bytes: number;
  digest: string;
  asOf: string;
  csvPath: string;
  health: BuildHealth;
  log: string;
  csv: string;
  readme: string;
}

function csvEscape(v: unknown): string {
  const s = v === null || v === undefined ? "" : String(v);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

function toCSV(rows: Array<Record<string, unknown>>, columns: string[]): string {
  const head = columns.map(csvEscape).join(",");
  const body = rows.map((r) => columns.map((c) => csvEscape(r[c])).join(","));
  return [head, ...body].join("\n") + "\n";
}

/** Health checks a design must clear. Each one exists because the failure it
 *  catches would otherwise reach an agent as confident-looking data. */
function health(rows: Array<Record<string, unknown>>, columns: string[], archetype: string): BuildHealth {
  const problems: string[] = [];
  const emptyColumns = columns.filter((c) => rows.every((r) => r[c] === null || r[c] === undefined || r[c] === ""));
  if (rows.length === 0) problems.push("查询结果为空，没有可用信息");
  const overCap = rows.length > limits.maxTableRows;
  if (overCap) problems.push(`行数 ${rows.length} 超过小表上限 ${limits.maxTableRows}，agent 无法快速读完`);
  if (emptyColumns.length) problems.push(`整列为空：${emptyColumns.join(", ")}`);

  // For a lookup table the leading column(s) are the key an agent will look up
  // by; duplicates there mean two answers to the same question.
  let duplicateKeys = 0;
  if (rows.length > 0 && (archetype === "dict" || archetype === "coverage")) {
    const keyCols = archetype === "dict" ? columns.slice(0, 1) : columns.slice(0, 2);
    const seen = new Set<string>();
    for (const r of rows) {
      const k = keyCols.map((c) => String(r[c])).join("\u0001");
      if (seen.has(k)) duplicateKeys++;
      seen.add(k);
    }
    if (duplicateKeys > 0) problems.push(`主键列 ${keyCols.join("+")} 有 ${duplicateKeys} 行重复`);
  }

  return { rows: rows.length, emptyColumns, duplicateKeys, overCap, ok: problems.length === 0, problems };
}

function renderReadme(d: TableDesign, r: { rows: number; asOf: string; digest: string }): string {
  return [
    `# ${d.name}`,
    "",
    d.intent,
    "",
    `- 原型：\`${d.archetype}\``,
    `- 粒度：${d.grain}`,
    `- 行数：${r.rows}`,
    `- as_of：${r.asOf}`,
    `- 刷新：\`${d.refresh_cron}\``,
    `- 内容摘要：\`${r.digest}\``,
    "",
    "## 列",
    "",
    "| 列 | 类型 | 含义 |",
    "|---|---|---|",
    ...d.columns.map((c) => `| \`${c.name}\` | ${c.type} | ${c.desc} |`),
    "",
    "## 来源",
    "",
    "```sql",
    d.source_sql,
    "```",
    "",
    `来源库：\`${d.source}\`（只读）。这张表由确定性作业构建，agent 只提出设计，不写数据。`,
    "",
    "## 已知边界",
    "",
    d.caveats_md || "（无）",
    "",
  ].join("\n");
}

/** Run one design. Never throws for a bad design — a rejection is a value the
 *  graph records, the same way a failed leaf is. */
export function drybuild(design: TableDesign): BuildResult {
  const log: string[] = [];
  const asOf = nowISO();
  const outDir = join(files.derived, design.name);

  const source = design.source === "patterns" ? db() : openWarehouse();
  const known = design.source === "patterns" ? ["query_patterns"] : Object.keys(warehouseSchema());
  let rows: Array<Record<string, unknown>> = [];
  try {
    assertReadOnlySelect(design.source_sql, known);
    const sql = withRowCap(design.source_sql, limits.sqlRowCap);
    log.push(`执行：${sql}`);
    rows = source.query(sql).all() as Array<Record<string, unknown>>;
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    log.push(`拒绝：${msg}`);
    return {
      ok: false,
      rows: 0,
      bytes: 0,
      digest: "",
      asOf,
      csvPath: "",
      health: { rows: 0, emptyColumns: [], duplicateKeys: 0, overCap: false, ok: false, problems: [msg] },
      log: log.join("\n"),
      csv: "",
      readme: "",
    };
  } finally {
    if (design.source !== "patterns") source.close();
  }

  // Trust the query's own column order, not the design's claim about it.
  const columns = rows.length ? Object.keys(rows[0]!) : design.columns.map((c) => c.name);
  const h = health(rows, columns, design.archetype);
  const csv = toCSV(rows, columns);
  const digest = createHash("sha256").update(csv).digest("hex").slice(0, 12);
  const readme = renderReadme(design, { rows: rows.length, asOf, digest });

  let csvPath = "";
  let bytes = 0;
  if (h.ok) {
    mkdirSync(outDir, { recursive: true });
    csvPath = join(outDir, `${design.name}.csv`);
    writeFileSync(csvPath, csv);
    writeFileSync(join(outDir, "README.md"), readme);
    bytes = statSync(csvPath).size;
    log.push(`构建通过：${rows.length} 行，${bytes} 字节`);
  } else {
    log.push(`体检未通过：${h.problems.join("；")}`);
  }

  return { ok: h.ok, rows: rows.length, bytes, digest, asOf, csvPath, health: h, log: log.join("\n"), csv, readme };
}
