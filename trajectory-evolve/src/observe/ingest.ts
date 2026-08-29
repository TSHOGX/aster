// observe/ingest.ts — raw trajectory JSONL -> trajectory_events. Zero LLM.
//
// The input format is the normalized 6-primitive shape the platform's trajectory
// parser already produces from pi's native session JSONL (user / assistant /
// thinking / tool_call / tool_result / system). Keeping ingest to a pure format
// mapping is what lets a re-ingest replace a session's rows wholesale without
// touching any distilled output.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { Database } from "bun:sqlite";
import { db, nowISO } from "../db.ts";

export interface IngestResult {
  sessionID: string;
  events: number;
  skipped: boolean;
}

interface RawLine {
  type: string;
  session_id?: string;
  user_id?: string;
  started_at?: string;
  day?: string;
  text?: string;
  tool?: string;
  args?: Record<string, unknown>;
  is_error?: boolean;
  rows?: number;
  latency_ms?: number;
}

const KINDS = new Set(["user", "assistant", "thinking", "tool_call", "tool_result", "system"]);

export function ingestFile(path: string): IngestResult {
  const body = readFileSync(path, "utf8");
  const fingerprint = createHash("sha256").update(body).digest("hex").slice(0, 16);
  const lines = body
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => JSON.parse(l) as RawLine);

  const header = lines.find((l) => l.type === "session");
  if (!header?.session_id) throw new Error(`${path}: missing a {"type":"session"} header line`);
  const sessionID = header.session_id;
  const startedAt = header.started_at ?? nowISO();
  const day = header.day ?? startedAt.slice(0, 10);

  const prior = db()
    .query<{ fingerprint: string }, [string]>(`SELECT fingerprint FROM sessions WHERE session_id = ?`)
    .get(sessionID);
  if (prior?.fingerprint === fingerprint) return { sessionID, events: 0, skipped: true };

  const d = db();
  d.transaction(() => {
    d.run(
      `INSERT INTO sessions (session_id, user_id, day, started_at, source_path, fingerprint, ingested_at)
       VALUES (?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(session_id) DO UPDATE SET
         user_id = excluded.user_id, day = excluded.day, started_at = excluded.started_at,
         source_path = excluded.source_path, fingerprint = excluded.fingerprint,
         ingested_at = excluded.ingested_at`,
      [sessionID, header.user_id ?? "unknown", day, startedAt, path, fingerprint, nowISO()]
    );
    // Rebuildable side: replace wholesale rather than merge, so a corrected
    // source file can never leave half-stale events behind.
    d.run(`DELETE FROM trajectory_events WHERE session_id = ?`, [sessionID]);

    let seq = 0;
    let turn = 0;
    const insert = d.query(
      `INSERT INTO trajectory_events (session_id, seq, at_turn, kind, text, tool, args_json, is_error, rows, latency_ms)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
    );
    for (const l of lines) {
      if (!KINDS.has(l.type)) continue;
      if (l.type === "user") turn++;
      insert.run(
        sessionID,
        seq++,
        turn,
        l.type,
        l.text ?? "",
        l.tool ?? null,
        l.args ? JSON.stringify(l.args) : null,
        l.is_error ? 1 : 0,
        l.rows ?? null,
        l.latency_ms ?? null
      );
    }
  })();

  const events = db()
    .query<{ n: number }, [string]>(`SELECT COUNT(*) AS n FROM trajectory_events WHERE session_id = ?`)
    .get(sessionID)!.n;
  return { sessionID, events, skipped: false };
}

/** Ingest every *.jsonl in a directory. Unchanged files are skipped by
 *  fingerprint, so a daily run costs nothing for sessions that did not move. */
export function ingestDir(dir: string): IngestResult[] {
  const out: IngestResult[] = [];
  for (const name of readdirSync(dir).sort()) {
    if (!name.endsWith(".jsonl")) continue;
    out.push(ingestFile(join(dir, name)));
  }
  return out;
}

/** Read the platform's `memory.db` (trajectory_snapshots + trajectory_events)
 *  and map each session into this pipeline's rebuildable tables.
 *
 *  Capture stays in the Go orchestrator (`backfill`). This function is the
 *  format bridge so cron/MCP can evolve from live sessions without a second
 *  parser. Fingerprint is the snapshot's own, so an unchanged session is a
 *  no-op here too. */
export function ingestMemoryDb(path: string): IngestResult[] {
  const src = new Database(path, { readonly: true, create: false });
  try {
    const snaps = src
      .query<
        { session_id: string; user_id: string; started_at: string | null; fingerprint: string },
        []
      >(
        `SELECT session_id, user_id, started_at, fingerprint FROM trajectory_snapshots ORDER BY session_id`
      )
      .all();
    const eventsQ = src.query<
      { kind: string; name: string | null; is_error: number; text: string },
      [string]
    >(
      `SELECT kind, name, is_error, text FROM trajectory_events WHERE session_id = ? ORDER BY idx`
    );
    const out: IngestResult[] = [];
    for (const snap of snaps) {
      out.push(ingestFromSnapshot(snap, eventsQ.all(snap.session_id)));
    }
    return out;
  } finally {
    src.close();
  }
}

function ingestFromSnapshot(
  snap: { session_id: string; user_id: string; started_at: string | null; fingerprint: string },
  events: Array<{ kind: string; name: string | null; is_error: number; text: string }>
): IngestResult {
  const sessionID = snap.session_id;
  const fingerprint = snap.fingerprint || createHash("sha256").update(JSON.stringify(events)).digest("hex").slice(0, 16);
  const prior = db()
    .query<{ fingerprint: string }, [string]>(`SELECT fingerprint FROM sessions WHERE session_id = ?`)
    .get(sessionID);
  if (prior?.fingerprint === fingerprint) return { sessionID, events: 0, skipped: true };

  const startedAt = snap.started_at ?? nowISO();
  const day = startedAt.slice(0, 10);
  const d = db();
  d.transaction(() => {
    d.run(
      `INSERT INTO sessions (session_id, user_id, day, started_at, source_path, fingerprint, ingested_at)
       VALUES (?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(session_id) DO UPDATE SET
         user_id = excluded.user_id, day = excluded.day, started_at = excluded.started_at,
         source_path = excluded.source_path, fingerprint = excluded.fingerprint,
         ingested_at = excluded.ingested_at`,
      [sessionID, snap.user_id || "unknown", day, startedAt, "memory.db", fingerprint, nowISO()]
    );
    d.run(`DELETE FROM trajectory_events WHERE session_id = ?`, [sessionID]);
    let seq = 0;
    let turn = 0;
    const insert = d.query(
      `INSERT INTO trajectory_events (session_id, seq, at_turn, kind, text, tool, args_json, is_error, rows, latency_ms)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
    );
    for (const e of events) {
      if (!KINDS.has(e.kind)) continue;
      if (e.kind === "user") turn++;
      let args: string | null = null;
      if (e.kind === "tool_call" && e.text) {
        try {
          JSON.parse(e.text);
          args = e.text;
        } catch {
          args = JSON.stringify({ text: e.text });
        }
      }
      insert.run(sessionID, seq++, turn, e.kind, e.text ?? "", e.name ?? null, args, e.is_error ? 1 : 0, null, null);
    }
  })();
  const n = db()
    .query<{ n: number }, [string]>(`SELECT COUNT(*) AS n FROM trajectory_events WHERE session_id = ?`)
    .get(sessionID)!.n;
  return { sessionID, events: n, skipped: false };
}
