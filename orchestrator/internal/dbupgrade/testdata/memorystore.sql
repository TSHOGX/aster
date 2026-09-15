CREATE TABLE agent_calls (
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
CREATE TABLE episode_memories (
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
CREATE TABLE evolution_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  phase0_plan_json TEXT NOT NULL DEFAULT '{}',
  counts_json TEXT NOT NULL DEFAULT '{}',
  digest TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT ''
);
CREATE TABLE extraction_state (
  session_id TEXT NOT NULL,
  stage TEXT NOT NULL,
  processed_turn_count INTEGER NOT NULL DEFAULT 0,
  processed_at TEXT,
  status TEXT NOT NULL DEFAULT 'ok' CHECK(status IN ('ok','error','skipped')),
  error TEXT,
  PRIMARY KEY(session_id, stage)
);
CREATE TABLE raw_sources (
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  kind TEXT NOT NULL,
  native_session_id TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  mtime TEXT,
  captured_at TEXT NOT NULL,
  PRIMARY KEY(session_id, path)
);
CREATE TABLE semantic_memories (
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
CREATE TABLE skill_candidates (
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
CREATE TABLE trajectory_events (
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
CREATE TABLE trajectory_snapshots (
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
CREATE INDEX episode_memories_session_idx
  ON episode_memories (session_id);
CREATE INDEX idx_agent_calls_run ON agent_calls(run_id);
CREATE INDEX semantic_memories_status_idx
  ON semantic_memories (status, scope);
CREATE UNIQUE INDEX skill_candidates_name_uq
  ON skill_candidates (target_skill_name);
CREATE INDEX skill_candidates_status_idx
  ON skill_candidates (status);
CREATE INDEX trajectory_events_kind_idx
  ON trajectory_events (session_id, kind);
CREATE INDEX trajectory_snapshots_extracted_idx
  ON trajectory_snapshots (extracted_at);
