package memorystore

import (
	"context"
	"strings"
)

// ensureSchema creates the 9 memory.db tables. This DDL is verbatim the DDL that
// used to live in internal/store/schema.go, minus the five cross-database
// FOREIGN KEY clauses documented below.
//
// ----------------------------------------------------------------------------
// CROSS-DATABASE REFERENCE
//
// trajectory_snapshots, trajectory_events, extraction_state, episode_memories
// and raw_sources each carried
//
//	FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
//
// against the control plane's sessions table. sessions now lives in a different
// database file, and SQLite has no cross-file foreign keys, so those clauses are
// gone. session_id here is an UNENFORCED soft key.
//
// Why that is safe today: nothing in the repository ever hard-DELETEs a row from
// sessions (or messages, or turns). Session teardown is a soft delete — a status
// flip to 'destroyed'/'failed' in DestroySession/FailSession — so these CASCADEs
// have never once fired. Dropping them removes an enforcement that was never
// exercised, not one the data depends on.
//
// OBLIGATION ON FUTURE WORK: if a session-purge / GDPR-erasure / retention-
// pruning feature is ever added that really does DELETE FROM sessions, it MUST
// delete this database's rows for that session_id explicitly. The database can
// no longer do it. The affected tables are exactly the five named above.
// ----------------------------------------------------------------------------
func (s *Store) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
-- ============================================================================
-- Trajectory distillation (agents-kb pipeline).
--
-- Two lifecycles share this database but must never be conflated (the sess-cli
-- trace.db/overlay.db split, expressed as table groups here):
--
--   * REBUILDABLE view  — trajectory_snapshots + trajectory_events. Derived
--     mechanically from the control plane's durable messages/message_traces/
--     message_feedback tables, read across the ingest boundary (see ingest.go).
--     Safe to delete and rebuild; a re-snapshot of a grown session replaces its
--     rows wholesale.
--   * NON-REBUILDABLE output — episode_memories, semantic_memories,
--     skill_candidates. Expensive LLM output keyed by session/evidence. A
--     re-snapshot of trajectory_events must NEVER cascade into these.
--
-- The two groups are joined only by soft keys (session_id, evidence refs), so
-- rebuilding the mechanical half leaves distilled memory untouched.
-- ============================================================================

-- One index row per snapshotted session. fingerprint = turn_count + last
-- message id/time; the nightly pipeline only re-normalizes and re-extracts a
-- session whose fingerprint moved since last run (sess-cli incremental model).
--
-- status/agent/user_id/started_at/ended_at are a POINT-IN-TIME COPY of control
-- plane columns, taken when the snapshot was written. There is no referential
-- integrity behind them and they are never refreshed except by a re-snapshot:
-- treat them as "what the session looked like when we last normalized it", not
-- as the session's current state. Anything needing live session state must read
-- the control plane.
CREATE TABLE IF NOT EXISTS trajectory_snapshots (
  session_id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  agent TEXT NOT NULL,
  user_id TEXT NOT NULL DEFAULT '',
  started_at TEXT,
  ended_at TEXT,
  turn_count INTEGER NOT NULL DEFAULT 0,
  message_count INTEGER NOT NULL DEFAULT 0,
  fingerprint TEXT NOT NULL DEFAULT '',
  fidelity TEXT NOT NULL DEFAULT 'full' CHECK(fidelity IN ('full','partial')),
  normalized_at TEXT,
  extracted_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS trajectory_snapshots_extracted_idx
  ON trajectory_snapshots (extracted_at);

-- Normalized event stream: the 7-primitive taxonomy borrowed from sess-cli
-- (user/assistant/thinking/tool_call/tool_result/system/usage). Derived from
-- messages.role (user/assistant) and message_traces.nodes[] (thinking/tool_*).
-- text is stored in full; large tool payloads already arrive summarized in the
-- trace nodes. Rebuildable: wiped+reinserted per session on re-snapshot.
--
-- message_id is a soft reference into the control plane's messages table, kept
-- for provenance only; nothing here joins on it.
CREATE TABLE IF NOT EXISTS trajectory_events (
  session_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('user','assistant','thinking','tool_call','tool_result','system','usage')),
  subtype TEXT,
  name TEXT,
  call_id TEXT,
  is_error INTEGER NOT NULL DEFAULT 0 CHECK(is_error IN (0,1)),
  native_id TEXT NOT NULL DEFAULT '',
  parent_id TEXT NOT NULL DEFAULT '',
  message_id INTEGER,
  tokens INTEGER,
  text TEXT NOT NULL,
  ts TEXT,
  PRIMARY KEY(session_id, idx)
);

CREATE INDEX IF NOT EXISTS trajectory_events_kind_idx
  ON trajectory_events (session_id, kind);

-- Per-(session, stage) watermark. processed_turn_count lets a lens detect when
-- a session has grown since it last ran and re-process only then. Stages are
-- the lens families: 'facts' | 'failure' | 'skill' | 'curator'.
CREATE TABLE IF NOT EXISTS extraction_state (
  session_id TEXT NOT NULL,
  stage TEXT NOT NULL,
  processed_turn_count INTEGER NOT NULL DEFAULT 0,
  processed_at TEXT,
  status TEXT NOT NULL DEFAULT 'ok' CHECK(status IN ('ok','error','skipped')),
  error TEXT,
  PRIMARY KEY(session_id, stage)
);

-- A structured summary of one session (ReasoningBank episodic item). Candidate
-- memories live in candidate_memories_json until consolidation lifts them into
-- semantic_memories. Non-rebuildable LLM output.
CREATE TABLE IF NOT EXISTS episode_memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  task TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT 'unknown' CHECK(outcome IN ('success','partial','failed','abandoned','unknown')),
  domains_json TEXT NOT NULL DEFAULT '[]',
  worked_steps_json TEXT NOT NULL DEFAULT '[]',
  failed_steps_json TEXT NOT NULL DEFAULT '[]',
  user_frustration TEXT NOT NULL DEFAULT 'unknown' CHECK(user_frustration IN ('none','mild','high','unknown')),
  candidate_memories_json TEXT NOT NULL DEFAULT '[]',
  evidence_json TEXT NOT NULL DEFAULT '[]',
  model TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS episode_memories_session_idx
  ON episode_memories (session_id);

-- Cross-session consolidated knowledge. support/contradiction counts drive
-- confidence; status gates promotion into the shared KB. Non-rebuildable.
CREATE TABLE IF NOT EXISTS semantic_memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL CHECK(type IN ('fact','heuristic','procedure','bug','preference')),
  scope TEXT NOT NULL DEFAULT 'global',
  statement TEXT NOT NULL,
  when_to_apply TEXT NOT NULL DEFAULT '',
  support_count INTEGER NOT NULL DEFAULT 1,
  contradiction_count INTEGER NOT NULL DEFAULT 0,
  evidence_session_ids_json TEXT NOT NULL DEFAULT '[]',
  confidence REAL NOT NULL DEFAULT 0.5,
  status TEXT NOT NULL DEFAULT 'candidate' CHECK(status IN ('candidate','accepted','deprecated')),
  canonical_of_json TEXT NOT NULL DEFAULT '[]',
  agent_rationale TEXT NOT NULL DEFAULT '',
  verify_verdict TEXT NOT NULL DEFAULT '',
  verify_checked_at TEXT,
  corrected_statement TEXT NOT NULL DEFAULT '',
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS semantic_memories_status_idx
  ON semantic_memories (status, scope);

-- A proposed change to the shared skills pack. body_md is human-reviewable
-- Markdown, not opaque model output. Non-rebuildable.
CREATE TABLE IF NOT EXISTS skill_candidates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  target_skill_name TEXT NOT NULL,
  operation TEXT NOT NULL DEFAULT 'create' CHECK(operation IN ('create','update','deprecate')),
  title TEXT NOT NULL DEFAULT '',
  body_md TEXT NOT NULL DEFAULT '',
  source_memory_ids_json TEXT NOT NULL DEFAULT '[]',
  evidence_session_ids_json TEXT NOT NULL DEFAULT '[]',
  support_count INTEGER NOT NULL DEFAULT 1,
  confidence REAL NOT NULL DEFAULT 0.5,
  status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','needs_review','accepted','published','rejected')),
  kb_status TEXT NOT NULL DEFAULT 'observing' CHECK(kb_status IN ('observing','in-use','deprecated')),
  curator_reason TEXT NOT NULL DEFAULT '',
  verify_verdict TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  published_at TEXT
);

CREATE INDEX IF NOT EXISTS skill_candidates_status_idx
  ON skill_candidates (status);

CREATE UNIQUE INDEX IF NOT EXISTS skill_candidates_name_uq
  ON skill_candidates (target_skill_name);

-- raw_sources points at the archived, verbatim native agent trace a session's
-- trajectory_events were parsed from (the sess-cli pointer model). It is NOT a
-- copy of the content — bytes/mtime exist for drift/staleness detection so the
-- ground-truth file can be re-read on demand. One pi session = one JSONL file =
-- one row; kind records the native format.
CREATE TABLE IF NOT EXISTS raw_sources (
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  kind TEXT NOT NULL,
  native_session_id TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  mtime TEXT,
  captured_at TEXT NOT NULL,
  PRIMARY KEY(session_id, path)
);

-- evolution_runs: one row per agentic context-evolution cycle. Powers Phase-0
-- "what changed since last cycle" and the daily report. Non-rebuildable output.
CREATE TABLE IF NOT EXISTS evolution_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  phase0_plan_json TEXT NOT NULL DEFAULT '{}',
  counts_json TEXT NOT NULL DEFAULT '{}',
  digest TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT ''
);

-- agent_calls: audit + cost/latency observability for every pi-agent invocation
-- across all nodes (reflect/consolidate/verify/curate/critic). Non-rebuildable.
CREATE TABLE IF NOT EXISTS agent_calls (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER,
  node TEXT NOT NULL,
  ref TEXT NOT NULL DEFAULT '',
  prompt_sha TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  ok INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_calls_run ON agent_calls(run_id);
`)
	if err != nil {
		return err
	}
	// The additive columns (native_id/parent_id on trajectory_events, the
	// agentic-evolution columns on semantic_memories/skill_candidates) are
	// declared inline in the CREATE TABLE above rather than added by an ALTER
	// pass. A memory.db created by this code is new, so there is no pre-column
	// shape to upgrade; the ALTER-based ensure*Columns helpers that used to run
	// against the shared control database are not carried over.
	//
	// The one-window data migration that copies an existing deployment's rows out
	// of control.db is deliberately NOT here: a schema path must not mutate
	// another database. It lives in the migration entrypoint.
	return s.ensureSemanticMemoryColumns(ctx)
}

// ensureSemanticMemoryColumns is the one forward-compat ALTER pass kept, for the
// case where a memory.db was created by the migration (which copies the control
// table's column set as it found it) rather than by the CREATE TABLE above. It
// is idempotent and O(1) metadata-only: ADD COLUMN with a constant DEFAULT does
// not rewrite the table.
func (s *Store) ensureSemanticMemoryColumns(ctx context.Context) error {
	columns := []struct {
		table string
		name  string
		ddl   string
	}{
		{"trajectory_events", "native_id", `ALTER TABLE trajectory_events ADD COLUMN native_id TEXT NOT NULL DEFAULT ''`},
		{"trajectory_events", "parent_id", `ALTER TABLE trajectory_events ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`},
		{"semantic_memories", "canonical_of_json", `ALTER TABLE semantic_memories ADD COLUMN canonical_of_json TEXT NOT NULL DEFAULT '[]'`},
		{"semantic_memories", "agent_rationale", `ALTER TABLE semantic_memories ADD COLUMN agent_rationale TEXT NOT NULL DEFAULT ''`},
		{"semantic_memories", "verify_verdict", `ALTER TABLE semantic_memories ADD COLUMN verify_verdict TEXT NOT NULL DEFAULT ''`},
		{"semantic_memories", "verify_checked_at", `ALTER TABLE semantic_memories ADD COLUMN verify_checked_at TEXT`},
		{"semantic_memories", "corrected_statement", `ALTER TABLE semantic_memories ADD COLUMN corrected_statement TEXT NOT NULL DEFAULT ''`},
		{"skill_candidates", "curator_reason", `ALTER TABLE skill_candidates ADD COLUMN curator_reason TEXT NOT NULL DEFAULT ''`},
		{"skill_candidates", "verify_verdict", `ALTER TABLE skill_candidates ADD COLUMN verify_verdict TEXT NOT NULL DEFAULT ''`},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, column.table, column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

// columnExists reports whether the given table has a column of the given name,
// via the pragma_table_info table-valued function, which accepts a bound
// parameter for the table name.
func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// MemoryTables is the authoritative list of the tables this database owns. The
// migration and the schema-separation tests both read it, so there is exactly
// one place that says which side a table belongs to.
var MemoryTables = []string{
	"trajectory_snapshots",
	"trajectory_events",
	"extraction_state",
	"episode_memories",
	"semantic_memories",
	"skill_candidates",
	"raw_sources",
	"evolution_runs",
	"agent_calls",
}

// quoteIdent quotes a SQLite identifier for the rare statement that cannot use a
// bound parameter (table names in the migration's DDL).
func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
