import { describe, expect, test } from "bun:test";
import { assertReadOnlySelect, normalizeSQL, withRowCap, type QueryShape } from "./sqlnorm.ts";

describe("fingerprinting", () => {
  test("queries differing only in literals share a fingerprint", () => {
    const a = normalizeSQL("SELECT dt, COUNT(*) FROM bms_telemetry WHERE model = 'EX5' GROUP BY dt");
    const b = normalizeSQL("SELECT dt, COUNT(*) FROM bms_telemetry WHERE model = 'ZS11' GROUP BY dt");
    // This is the property the whole pain view rests on: "the same question
    // asked again with different constants" must collapse to one key.
    expect(a.sha).toBe(b.sha);
  });

  test("a different column is a different fingerprint", () => {
    const a = normalizeSQL("SELECT DISTINCT work_condition FROM bms_telemetry");
    const b = normalizeSQL("SELECT DISTINCT model FROM bms_telemetry");
    expect(a.sha).not.toBe(b.sha);
  });

  test("tables are extracted from FROM and JOIN", () => {
    const q = normalizeSQL(
      "SELECT AVG(t.current_a) FROM bms_telemetry t JOIN charge_session c ON t.vehicle_id = c.vehicle_id"
    );
    expect(q.tables).toEqual(["bms_telemetry", "charge_session"]);
  });
});

describe("shape classification", () => {
  const cases: Array<[string, QueryShape]> = [
    ["SELECT DISTINCT work_condition FROM bms_telemetry WHERE dt >= '2026-08-12'", "enum_probe"],
    ["SELECT MIN(ts), MAX(ts) FROM bms_telemetry WHERE model = 'EX5'", "coverage_probe"],
    ["SELECT dt, COUNT(*) FROM bms_telemetry GROUP BY dt", "coverage_probe"],
    ["SELECT column_name FROM information_schema.columns WHERE table_name = 'x'", "schema_probe"],
    ["SELECT AVG(soc) FROM bms_telemetry t JOIN charge_session c ON t.vehicle_id = c.vehicle_id", "analysis"],
  ];
  for (const [sql, shape] of cases) {
    test(`${shape}: ${sql.slice(0, 40)}`, () => {
      expect(normalizeSQL(sql).shape).toBe(shape);
    });
  }
});

describe("source_sql guardrails", () => {
  const known = ["bms_telemetry", "charge_session"];

  test("accepts a single read-only SELECT over known tables", () => {
    expect(() =>
      assertReadOnlySelect("SELECT work_condition, COUNT(*) FROM bms_telemetry GROUP BY work_condition", known)
    ).not.toThrow();
  });

  test("rejects writes, DDL, extra statements and unknown tables", () => {
    expect(() => assertReadOnlySelect("DELETE FROM bms_telemetry", known)).toThrow();
    expect(() => assertReadOnlySelect("SELECT 1; DROP TABLE bms_telemetry", known)).toThrow();
    expect(() => assertReadOnlySelect("SELECT * FROM secrets", known)).toThrow(/unknown table/);
    expect(() =>
      assertReadOnlySelect("SELECT * FROM bms_telemetry; INSERT INTO x VALUES (1)", known)
    ).toThrow();
  });

  test("row cap is added when absent and left alone when present", () => {
    expect(withRowCap("SELECT * FROM t", 100)).toBe("SELECT * FROM t LIMIT 100");
    expect(withRowCap("SELECT * FROM t LIMIT 5", 100)).toBe("SELECT * FROM t LIMIT 5");
  });
});
