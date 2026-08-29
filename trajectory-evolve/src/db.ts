// db.ts — the single SQLite state store.
//
// Two table groups with opposite lifecycles, kept apart on purpose:
//
//   REBUILDABLE  trajectory_events, session_observations, kb_touch,
//                query_observations, query_patterns, retrieval_gaps
//                Derived mechanically from raw trajectories. A re-ingest
//                replaces a session's rows wholesale; nothing is lost.
//
//   NON-REBUILDABLE  semantic_memories, skill_candidates, extractor_versions,
//                extractor_evals, table_candidates, table_builds, table_evals
//                Expensive agent output plus human decisions. A re-ingest must
//                never cascade into these; they join the rebuildable side by
//                soft keys (session id, evidence refs) only.

import { Database } from "bun:sqlite";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { files } from "./config.ts";

const SCHEMA = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------- rebuildable

CREATE TABLE IF NOT EXISTS sessions (
  session_id   TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL DEFAULT 'unknown',
  day          TEXT NOT NULL,               -- YYYY-MM-DD (local business day)
  started_at   TEXT NOT NULL,
  source_path  TEXT NOT NULL,
  fingerprint  TEXT NOT NULL,               -- content hash; unchanged => skip
  ingested_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS trajectory_events (
  session_id TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  at_turn    INTEGER NOT NULL,
  kind       TEXT NOT NULL,                 -- user|assistant|thinking|tool_call|tool_result|system
  text       TEXT NOT NULL DEFAULT '',
  tool       TEXT,
  args_json  TEXT,
  is_error   INTEGER NOT NULL DEFAULT 0,
  rows       INTEGER,
  latency_ms INTEGER,
  PRIMARY KEY (session_id, seq)
);

-- One row per session: the cheap summary every downstream gate reads.
CREATE TABLE IF NOT EXISTS session_observations (
  session_id       TEXT PRIMARY KEY,
  day              TEXT NOT NULL,
  user_id          TEXT NOT NULL,
  turn_count       INTEGER NOT NULL,
  outcome_signal   TEXT NOT NULL,           -- success|partial|failed|abandoned|unknown
  error_count      INTEGER NOT NULL,
  correction_count INTEGER NOT NULL,
  query_count      INTEGER NOT NULL,
  kb_touch_count   INTEGER NOT NULL,
  kb_miss_count    INTEGER NOT NULL,
  probe_chain_len  INTEGER NOT NULL,        -- longest run of exploratory queries
  observed_at      TEXT NOT NULL
);

-- Every read/grep against the mounted KB. A matched=0 row is direct evidence
-- of an extraction gap: the agent looked, and the knowledge was not there.
CREATE TABLE IF NOT EXISTS kb_touch (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  at_turn    INTEGER NOT NULL,
  path       TEXT NOT NULL,
  mode       TEXT NOT NULL,                 -- read|grep|glob
  query      TEXT NOT NULL DEFAULT '',
  matched    INTEGER NOT NULL
);

-- Every data query, with literals stripped so "the same question asked again"
-- is a GROUP BY rather than an LLM call.
CREATE TABLE IF NOT EXISTS query_observations (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  at_turn    INTEGER NOT NULL,
  engine     TEXT NOT NULL,                 -- doris|duckdb|shell
  sql_raw    TEXT NOT NULL,
  sql_norm   TEXT NOT NULL,
  sql_sha    TEXT NOT NULL,
  shape      TEXT NOT NULL,                 -- enum_probe|coverage_probe|schema_probe|analysis|other
  tables_json  TEXT NOT NULL,
  columns_json TEXT NOT NULL,
  filters_json TEXT NOT NULL,
  status     TEXT NOT NULL,                 -- ok|error
  error_class TEXT,
  rows       INTEGER,
  latency_ms INTEGER
);

-- Daily rollup of query_observations by fingerprint.
CREATE TABLE IF NOT EXISTS query_patterns (
  sql_sha      TEXT PRIMARY KEY,
  sql_norm     TEXT NOT NULL,
  shape        TEXT NOT NULL,
  sessions     INTEGER NOT NULL,
  occurrences  INTEGER NOT NULL,
  err_rate     REAL NOT NULL,
  avg_latency  REAL NOT NULL,
  tables_json  TEXT NOT NULL,
  columns_json TEXT NOT NULL,
  first_seen   TEXT NOT NULL,
  last_seen    TEXT NOT NULL
);

-- What the KB failed to answer. Feeds track A's "miss" quadrant.
CREATE TABLE IF NOT EXISTS retrieval_gaps (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  day        TEXT NOT NULL,
  session_id TEXT NOT NULL,
  kind       TEXT NOT NULL,                 -- searched_no_match|never_searched_then_failed
  query      TEXT NOT NULL,
  scope_hint TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT ''
);

-- ------------------------------------------------------------ non-rebuildable

CREATE TABLE IF NOT EXISTS semantic_memories (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  type               TEXT NOT NULL,         -- fact|heuristic|procedure|bug|preference
  scope              TEXT NOT NULL,
  canonical_scope    TEXT NOT NULL DEFAULT '',
  statement          TEXT NOT NULL,
  when_to_apply      TEXT NOT NULL DEFAULT '',
  support_count      INTEGER NOT NULL DEFAULT 1,
  contradiction_count INTEGER NOT NULL DEFAULT 0,
  evidence_json      TEXT NOT NULL DEFAULT '[]',
  canonical_of_json  TEXT NOT NULL DEFAULT '[]',
  status             TEXT NOT NULL DEFAULT 'candidate',  -- candidate|accepted|deprecated
  verify_verdict     TEXT,                  -- current|stale|fixed|unverifiable
  corrected_statement TEXT,
  agent_rationale    TEXT,
  extractor_version_id INTEGER,
  shadow             INTEGER NOT NULL DEFAULT 0,  -- 1 = produced by a candidate extractor, never published
  first_seen_at      TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  UNIQUE (type, scope, statement, shadow, extractor_version_id)
);

CREATE TABLE IF NOT EXISTS skill_candidates (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL,
  title         TEXT NOT NULL,
  scope         TEXT NOT NULL,
  body_md       TEXT NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '[]',
  support_count INTEGER NOT NULL DEFAULT 1,
  status        TEXT NOT NULL DEFAULT 'draft',      -- draft|published|rejected
  kb_status     TEXT NOT NULL DEFAULT 'observing',  -- observing|in-use|deprecated
  curator_reason TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  UNIQUE (name)
);

-- Track A: the extraction method itself, versioned.
CREATE TABLE IF NOT EXISTS extractor_versions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  lens         TEXT NOT NULL,               -- facts|failure|skill
  version      TEXT NOT NULL,
  prompt_sha   TEXT NOT NULL,
  prompt_md    TEXT NOT NULL,
  schema_json  TEXT NOT NULL,
  gates_json   TEXT NOT NULL,
  slice_json   TEXT NOT NULL,
  status       TEXT NOT NULL,               -- draft|shadow|active|retired
  rationale    TEXT NOT NULL DEFAULT '',
  parent_id    INTEGER,
  created_at   TEXT NOT NULL,
  activated_at TEXT,
  UNIQUE (lens, version)
);

CREATE TABLE IF NOT EXISTS extractor_evals (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id        INTEGER NOT NULL,
  version_id    INTEGER NOT NULL,
  precision_pct REAL NOT NULL,
  gap_recall    REAL NOT NULL,
  redundancy    REAL NOT NULL,
  contract_violations INTEGER NOT NULL,
  judge_json    TEXT NOT NULL,
  verdict       TEXT NOT NULL,              -- promote|hold|reject
  created_at    TEXT NOT NULL
);

-- Track B: working-condition lookup tables.
CREATE TABLE IF NOT EXISTS table_candidates (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id         INTEGER NOT NULL,
  domain         TEXT NOT NULL,
  name           TEXT NOT NULL,
  intent         TEXT NOT NULL,
  archetype      TEXT NOT NULL,             -- coverage|template|dict|range|map
  evidence_json  TEXT NOT NULL,
  pain_json      TEXT NOT NULL,
  schema_json    TEXT NOT NULL,
  source_sql     TEXT NOT NULL,
  grain          TEXT NOT NULL,
  refresh_cron   TEXT NOT NULL,
  caveats_md     TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,             -- designed|built|validated|staged|live|rejected
  reject_reason  TEXT,
  agent_rationale TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS table_builds (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  candidate_id INTEGER NOT NULL,
  digest       TEXT NOT NULL,
  rows         INTEGER NOT NULL,
  bytes        INTEGER NOT NULL,
  as_of        TEXT NOT NULL,
  health_json  TEXT NOT NULL,
  ok           INTEGER NOT NULL,
  log          TEXT NOT NULL DEFAULT '',
  csv_path     TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS table_evals (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  candidate_id INTEGER NOT NULL,
  replay_json  TEXT NOT NULL,
  before_json  TEXT NOT NULL,
  after_json   TEXT NOT NULL,
  judge_json   TEXT NOT NULL,
  verdict      TEXT NOT NULL,               -- useful|no_gain|harmful
  created_at   TEXT NOT NULL
);

-- Which knowledge actually got used, per day. The 'unused' quadrant.
CREATE TABLE IF NOT EXISTS kb_usage (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  day        TEXT NOT NULL,
  session_id TEXT NOT NULL,
  kind       TEXT NOT NULL,                 -- memory|skill|table
  ref        TEXT NOT NULL,
  helped     INTEGER                        -- NULL = unknown (no judgment yet)
);

-- ------------------------------------------------------------------- audit

CREATE TABLE IF NOT EXISTS evolution_runs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  track       TEXT NOT NULL,                -- observe|kb|tables|extractors
  plan_json   TEXT NOT NULL DEFAULT '{}',
  counts_json TEXT NOT NULL DEFAULT '{}',
  digest      TEXT NOT NULL DEFAULT '',
  notes       TEXT NOT NULL DEFAULT '',
  started_at  TEXT NOT NULL,
  finished_at TEXT
);

CREATE TABLE IF NOT EXISTS agent_calls (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER NOT NULL,
  node       TEXT NOT NULL,
  ref        TEXT NOT NULL DEFAULT '',
  engine     TEXT NOT NULL,
  model      TEXT NOT NULL DEFAULT '',
  prompt_sha TEXT NOT NULL,
  ok         INTEGER NOT NULL,
  reason     TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_session ON trajectory_events(session_id);
CREATE INDEX IF NOT EXISTS idx_qobs_sha ON query_observations(sql_sha);
CREATE INDEX IF NOT EXISTS idx_kbtouch_session ON kb_touch(session_id);
CREATE INDEX IF NOT EXISTS idx_mem_status ON semantic_memories(status, shadow);
CREATE INDEX IF NOT EXISTS idx_usage_day ON kb_usage(day);
`;

let handle: Database | undefined;

export function db(): Database {
  if (handle) return handle;
  mkdirSync(dirname(files.db), { recursive: true });
  const d = new Database(files.db, { create: true });
  d.exec(SCHEMA);
  handle = d;
  return d;
}

export function nowISO(): string {
  return new Date().toISOString();
}

/** Open an audit row for one graph run. Every node writes against it. */
export function startRun(track: string, plan: unknown = {}): number {
  const r = db()
    .query<{ id: number }, [string, string, string]>(
      `INSERT INTO evolution_runs (track, plan_json, started_at) VALUES (?, ?, ?) RETURNING id`
    )
    .get(track, JSON.stringify(plan), nowISO());
  return r!.id;
}

export function finishRun(
  runID: number,
  counts: Record<string, number>,
  digest = "",
  notes = ""
): void {
  db().run(
    `UPDATE evolution_runs SET counts_json = ?, digest = ?, notes = ?, finished_at = ? WHERE id = ?`,
    [JSON.stringify(counts), digest, notes, nowISO(), runID]
  );
}

export function recordAgentCall(row: {
  runID: number;
  node: string;
  ref?: string;
  engine: string;
  model?: string;
  promptSha: string;
  ok: boolean;
  reason?: string;
  latencyMs: number;
}): void {
  db().run(
    `INSERT INTO agent_calls (run_id, node, ref, engine, model, prompt_sha, ok, reason, latency_ms, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [
      row.runID,
      row.node,
      row.ref ?? "",
      row.engine,
      row.model ?? "",
      row.promptSha,
      row.ok ? 1 : 0,
      row.reason ?? "",
      Math.round(row.latencyMs),
      nowISO(),
    ]
  );
}
