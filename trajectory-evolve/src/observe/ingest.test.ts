import { expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { db } from "../db.ts";
import { ingestMemoryDb } from "./ingest.ts";

test("ingestMemoryDb maps platform snapshots and skips unchanged fingerprints", () => {
  const dir = mkdtempSync(join(tmpdir(), "te-memory-"));
  const path = join(dir, "memory.db");
  const src = new Database(path);
  src.exec(`
    CREATE TABLE trajectory_snapshots (
      session_id TEXT PRIMARY KEY,
      user_id TEXT NOT NULL DEFAULT '',
      started_at TEXT,
      fingerprint TEXT NOT NULL DEFAULT ''
    );
    CREATE TABLE trajectory_events (
      session_id TEXT NOT NULL,
      idx INTEGER NOT NULL,
      kind TEXT NOT NULL,
      name TEXT,
      is_error INTEGER NOT NULL DEFAULT 0,
      text TEXT NOT NULL,
      PRIMARY KEY(session_id, idx)
    );
    INSERT INTO trajectory_snapshots VALUES ('sess_mcp', 'u1', '2026-08-20T01:00:00Z', 'fp1');
    INSERT INTO trajectory_events VALUES ('sess_mcp', 0, 'user', NULL, 0, '有哪些工况');
    INSERT INTO trajectory_events VALUES ('sess_mcp', 1, 'tool_call', 'executeQuery', 0, '{"sql":"SELECT 1"}');
    INSERT INTO trajectory_events VALUES ('sess_mcp', 2, 'tool_result', 'executeQuery', 0, '1');
    INSERT INTO trajectory_events VALUES ('sess_mcp', 3, 'usage', NULL, 0, 'tokens');
    INSERT INTO trajectory_events VALUES ('sess_mcp', 4, 'assistant', NULL, 0, '三种');
  `);
  src.close();

  db();
  const first = ingestMemoryDb(path);
  expect(first).toEqual([{ sessionID: "sess_mcp", events: 4, skipped: false }]);

  const row = db()
    .query<{ tool: string | null; kind: string }, [string]>(
      `SELECT kind, tool FROM trajectory_events WHERE session_id = ? AND kind = 'tool_call'`
    )
    .get("sess_mcp");
  expect(row?.tool).toBe("executeQuery");

  const second = ingestMemoryDb(path);
  expect(second[0]?.skipped).toBe(true);

  rmSync(dir, { recursive: true, force: true });
});
