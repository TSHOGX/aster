import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import {
  applyQueryOutput,
  extractRows,
  resolveWorkspaceOutputPath,
  rowsToCsv,
  stripOutputParam,
  withFileOutputSchema,
  QueryOutputError,
} from "../query-output.ts";

test("withFileOutputSchema adds optional output next to sql", () => {
  const schema = withFileOutputSchema({
    type: "object",
    properties: { sql: { type: "string" } },
    required: ["sql"],
    additionalProperties: false,
  });
  const props = schema["properties"] as Record<string, unknown>;
  assert.equal((props["sql"] as { type: string }).type, "string");
  assert.equal((props["output"] as { type: string }).type, "string");
  assert.deepEqual(schema["required"], ["sql"]);
});

test("stripOutputParam removes output before upstream arguments", () => {
  const { arguments: args, output } = stripOutputParam({
    sql: "SELECT 1",
    output: "file:///workspace/data/q.csv",
  });
  assert.deepEqual(args, { sql: "SELECT 1" });
  assert.equal(output, "file:///workspace/data/q.csv");
});

test("resolveWorkspaceOutputPath accepts file URL and relative path", () => {
  const root = "/workspace";
  assert.equal(
    resolveWorkspaceOutputPath("file:///workspace/data/q.csv", root),
    path.resolve("/workspace/data/q.csv"),
  );
  assert.equal(
    resolveWorkspaceOutputPath("/workspace/data/q.csv", root),
    path.resolve("/workspace/data/q.csv"),
  );
  assert.equal(
    resolveWorkspaceOutputPath("data/q.csv", root),
    path.resolve("/workspace/data/q.csv"),
  );
});

test("resolveWorkspaceOutputPath rejects traversal and foreign hosts", () => {
  assert.throws(
    () => resolveWorkspaceOutputPath("file:///etc/passwd", "/workspace"),
    QueryOutputError,
  );
  assert.throws(
    () => resolveWorkspaceOutputPath("file:///workspace/../etc/passwd", "/workspace"),
    QueryOutputError,
  );
  assert.throws(
    () => resolveWorkspaceOutputPath("file://evil.example/workspace/x.csv", "/workspace"),
    QueryOutputError,
  );
  assert.throws(
    () => resolveWorkspaceOutputPath("/workspace", "/workspace"),
    QueryOutputError,
  );
});

test("extractRows handles array, data, and wrapped result string", () => {
  assert.deepEqual(extractRows([{ a: 1 }, { a: 2 }]), [{ a: 1 }, { a: 2 }]);
  assert.deepEqual(extractRows({ rowCount: 1, data: [{ a: 1 }] }), [{ a: 1 }]);
  assert.deepEqual(
    extractRows({ result: JSON.stringify([{ a: 1 }]) }),
    [{ a: 1 }],
  );
  assert.deepEqual(extractRows({ result: { rows: [{ a: 1 }] } }), [{ a: 1 }]);
});

test("rowsToCsv escapes commas and quotes", () => {
  const csv = rowsToCsv([{ name: 'a, "b"', n: 1 }]);
  assert.equal(csv, 'name,n\n"a, ""b""",1\n');
});

test("applyQueryOutput lands CSV and returns a short summary", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mcp-output-"));
  const dest = path.join(dir, "data", "raw.csv");
  const rows = [
    { id: 1, name: "alpha" },
    { id: 2, name: "beta" },
    { id: 3, name: "gamma" },
    { id: 4, name: "delta" },
  ];
  const result = applyQueryOutput(
    [{ type: "text", text: JSON.stringify({ rowCount: 4, data: rows }) }],
    { workspaceRoot: dir, output: dest, sampleRows: 3 },
  );
  assert.equal(result.landed, true);
  assert.equal(result.auto, false);
  const summary = JSON.parse(result.content[0]!.text);
  assert.equal(summary.path, dest);
  assert.equal(summary.rowCount, 4);
  assert.deepEqual(summary.cols, ["id", "name"]);
  assert.equal(summary.sample.length, 3);
  const csv = fs.readFileSync(dest, "utf8");
  assert.match(csv, /^id,name\n1,alpha\n/);
  assert.equal(csv.trim().split("\n").length, 5);
});

test("applyQueryOutput leaves small peeks inline", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mcp-output-"));
  const peek = JSON.stringify({ data: [{ n: 1 }] });
  const result = applyQueryOutput([{ type: "text", text: peek }], {
    workspaceRoot: dir,
  });
  assert.equal(result.landed, false);
  assert.equal(result.content[0]!.text, peek);
});

test("applyQueryOutput auto-lands oversized replies without output", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mcp-output-"));
  const rows = Array.from({ length: 25 }, (_, i) => ({ i }));
  const result = applyQueryOutput(
    [{ type: "text", text: JSON.stringify({ data: rows }) }],
    { workspaceRoot: dir, now: new Date("2026-09-02T08:20:15.000Z") },
  );
  assert.equal(result.landed, true);
  assert.equal(result.auto, true);
  const summary = JSON.parse(result.content[0]!.text);
  assert.equal(summary.rowCount, 25);
  assert.ok(fs.existsSync(summary.path));
  assert.match(summary.path, /query_20260902T082015000Z\.csv$/);
});
