// Integration test for the deterministic half of the table track: a real query
// against a real (fixture) warehouse, and the health checks that decide whether
// a design ships. TE_STATE_ROOT is set before the imports because config
// resolves its paths once at module evaluation.

import { describe, expect, test } from "bun:test";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

process.env.TE_STATE_ROOT = mkdtempSync(join(tmpdir(), "te-test-"));

const { buildWarehouse, warehouseSchema } = await import("../warehouse.ts");
const { drybuild } = await import("./drybuild.ts");

buildWarehouse();

const base = {
  domain: "battery",
  source: "warehouse" as const,
  grain: "test",
  refresh_cron: "0 2 * * *",
  caveats_md: "",
};

describe("drybuild", () => {
  test("builds a coverage table and writes CSV + README", () => {
    const r = drybuild({
      ...base,
      name: "t_coverage",
      archetype: "coverage",
      intent: "按分区给出覆盖情况",
      columns: [
        { name: "dt", type: "TEXT", desc: "分区" },
        { name: "work_condition", type: "TEXT", desc: "工况码" },
        { name: "rows_cnt", type: "INTEGER", desc: "样本数" },
      ],
      source_sql:
        "SELECT dt, work_condition, COUNT(*) AS rows_cnt FROM bms_telemetry GROUP BY dt, work_condition",
    });
    expect(r.ok).toBe(true);
    expect(r.rows).toBeGreaterThan(0);
    expect(r.csv.split("\n")[0]).toBe("dt,work_condition,rows_cnt");
    expect(r.readme).toContain("## 已知边界");
    expect(r.digest).toHaveLength(12);
  });

  test("the same design twice yields the same digest", () => {
    const design = {
      ...base,
      name: "t_dict",
      archetype: "dict",
      intent: "枚举",
      columns: [{ name: "code", type: "TEXT", desc: "工况码" }],
      source_sql: "SELECT work_condition AS code, COUNT(*) AS samples FROM bms_telemetry GROUP BY work_condition",
    };
    expect(drybuild(design).digest).toBe(drybuild(design).digest);
  });

  test("rejects a design whose SQL is not a read-only SELECT", () => {
    const r = drybuild({
      ...base,
      name: "t_bad",
      archetype: "dict",
      intent: "坏设计",
      columns: [],
      source_sql: "DROP TABLE bms_telemetry",
    });
    expect(r.ok).toBe(false);
    expect(r.health.problems.join(" ")).toMatch(/SELECT/);
  });

  test("rejects a design that produces an empty table", () => {
    const r = drybuild({
      ...base,
      name: "t_empty",
      archetype: "dict",
      intent: "空表",
      columns: [{ name: "code", type: "TEXT", desc: "" }],
      source_sql: "SELECT work_condition AS code FROM bms_telemetry WHERE work_condition = 'NOPE'",
    });
    expect(r.ok).toBe(false);
    expect(r.health.rows).toBe(0);
  });

  test("rejects duplicate lookup keys — two answers to one question", () => {
    const r = drybuild({
      ...base,
      name: "t_dupe",
      archetype: "dict",
      intent: "主键重复",
      columns: [{ name: "code", type: "TEXT", desc: "" }],
      // No GROUP BY: one row per sample, so the lookup key repeats.
      source_sql: "SELECT work_condition AS code FROM bms_telemetry",
    });
    expect(r.ok).toBe(false);
    expect(r.health.duplicateKeys).toBeGreaterThan(0);
  });

  test("warehouse schema exposes the tables a design may reference", () => {
    const schema = warehouseSchema();
    expect(Object.keys(schema).sort()).toEqual(["bms_telemetry", "charge_session", "vehicle_model"]);
    expect(schema.bms_telemetry).toContain("work_condition");
  });
});
