package dbsplit_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/store"

	_ "modernc.org/sqlite"
)

// controlTables are non-memory tables seeded by the fixture and asserted
// untouched across every migration. sessions is the FK parent for 5 of the
// moved tables; messages/message_traces additionally exercise the messages_fts
// virtual table and its AFTER INSERT/UPDATE/DELETE sync triggers, so we would
// notice if a migration disturbed FTS shadow tables.
var controlTables = []string{"users", "sessions", "messages", "message_traces", "events", "turns"}

// seededSessions are the session ids the memory rows reference.
var seededSessions = []string{"sess-alpha", "sess-beta", "sess-gamma"}

// newFixture builds an authentic orchestrator.db by running the real store's
// Open on a temp path. Using the production code path (rather than a hand-copied
// DDL literal) is what guarantees the fixture carries the incremental
// ALTER TABLE ADD COLUMN migrations: trajectory_events.native_id/parent_id,
// semantic_memories.canonical_of_json and friends, skill_candidates.curator_reason.
// It then seeds rows into all 9 memory tables and the control tables.
//
// Returns the control DB path and the memory DB path (which does not exist yet).
func newFixture(t *testing.T) (controlPath, memoryPath string) {
	t.Helper()
	dir := t.TempDir()
	controlPath = filepath.Join(dir, "orchestrator.db")
	memoryPath = filepath.Join(dir, "memory.db")

	ctx := context.Background()
	st, err := store.Open(ctx, controlPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.EnsureUser(ctx, "lab", "Lab User"); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	db := openRaw(t, controlPath)
	defer db.Close()
	createLegacyMemoryTables(t, db)
	seed(t, db)
	return controlPath, memoryPath
}

// createLegacyMemoryTables recreates the 9 memory tables as they existed in
// orchestrator.db BEFORE the control/memory split, including the 5 FKs into
// sessions(id) and the columns that were added by incremental ALTER migrations
// (trajectory_events.native_id/parent_id, semantic_memories.canonical_of_json
// and friends, skill_candidates.curator_reason/verify_verdict).
//
// This DDL is deliberately a literal here rather than being obtained from
// store.Open: the current control schema no longer creates these tables, so the
// production code path can no longer produce a pre-split database. The migration
// under test runs against OLD production files, so the fixture has to reproduce
// that historical shape. Copied verbatim from internal/store/schema.go as of
// commit 02c004f (the last commit before the split), with the ALTER-added
// columns folded in at the position ALTER TABLE would have appended them.
func createLegacyMemoryTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(legacyMemorySchema); err != nil {
		t.Fatalf("create legacy memory tables: %v", err)
	}
}

const legacyMemorySchema = `
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
  created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS trajectory_snapshots_extracted_idx
  ON trajectory_snapshots (extracted_at);

CREATE TABLE IF NOT EXISTS trajectory_events (
  session_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('user','assistant','thinking','tool_call','tool_result','system','usage')),
  subtype TEXT,
  name TEXT,
  call_id TEXT,
  is_error INTEGER NOT NULL DEFAULT 0 CHECK(is_error IN (0,1)),
  message_id INTEGER,
  tokens INTEGER,
  text TEXT NOT NULL,
  ts TEXT,
  native_id TEXT NOT NULL DEFAULT '',
  parent_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(session_id, idx),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS trajectory_events_kind_idx
  ON trajectory_events (session_id, kind);

CREATE TABLE IF NOT EXISTS extraction_state (
  session_id TEXT NOT NULL,
  stage TEXT NOT NULL,
  processed_turn_count INTEGER NOT NULL DEFAULT 0,
  processed_at TEXT,
  status TEXT NOT NULL DEFAULT 'ok' CHECK(status IN ('ok','error','skipped')),
  error TEXT,
  PRIMARY KEY(session_id, stage),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

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
  created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS episode_memories_session_idx
  ON episode_memories (session_id);

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
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  canonical_of_json TEXT NOT NULL DEFAULT '[]',
  agent_rationale TEXT NOT NULL DEFAULT '',
  verify_verdict TEXT NOT NULL DEFAULT '',
  verify_checked_at TEXT,
  corrected_statement TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS semantic_memories_status_idx
  ON semantic_memories (status, scope);

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
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  published_at TEXT,
  curator_reason TEXT NOT NULL DEFAULT '',
  verify_verdict TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS skill_candidates_status_idx
  ON skill_candidates (status);

CREATE UNIQUE INDEX IF NOT EXISTS skill_candidates_name_uq
  ON skill_candidates (target_skill_name);

CREATE TABLE IF NOT EXISTS raw_sources (
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  kind TEXT NOT NULL,
  native_session_id TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  mtime TEXT,
  captured_at TEXT NOT NULL,
  PRIMARY KEY(session_id, path),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS evolution_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  phase0_plan_json TEXT NOT NULL DEFAULT '{}',
  counts_json TEXT NOT NULL DEFAULT '{}',
  digest TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT ''
);

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
`

// openRaw opens a SQLite file directly, with FKs ON so seeding and the orphan
// rejection test exercise real enforcement.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000`); err != nil {
		t.Fatalf("pragmas: %v", err)
	}
	return db
}

func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %.60q: %v", query, err)
		}
	}

	// ---- control-plane rows (must survive every migration untouched) ----
	for i, sid := range seededSessions {
		exec(`INSERT INTO sessions (id, user_id, status, driver_id, mode, workspace, restore_id, created_at, updated_at)
		      VALUES (?, 'lab', 'destroyed', 'pi', 'agent', ?, ?, ?, ?)`,
			sid, "/ws/"+sid, fmt.Sprintf("restore-%d", i), now, now)
		// messages + message_traces fire the messages_fts triggers.
		for m := 0; m < 3; m++ {
			role := "user"
			if m%2 == 1 {
				role = "assistant"
			}
			res, err := db.Exec(`INSERT INTO messages (session_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
				sid, role, fmt.Sprintf("message %d of %s about batteries", m, sid), now)
			if err != nil {
				t.Fatalf("seed message: %v", err)
			}
			id, _ := res.LastInsertId()
			if role == "assistant" {
				exec(`INSERT INTO message_traces (message_id, session_id, nodes, created_at) VALUES (?, ?, ?, ?)`,
					id, sid, `[{"kind":"thinking","text":"consider"}]`, now)
			}
		}
		exec(`INSERT INTO events (session_id, type, payload, created_at) VALUES (?, 'session.created', '{}', ?)`,
			sid, now)
		exec(`INSERT INTO turns (session_id, sequence, role, content, status, created_at)
		      VALUES (?, ?, 'user', 'hello', 'completed', ?)`, sid, i+1, now)
	}

	// ---- memory tables (the 9 that move) ----
	for i, sid := range seededSessions {
		exec(`INSERT INTO trajectory_snapshots (session_id, status, agent, user_id, turn_count, message_count, fingerprint, fidelity, created_at)
		      VALUES (?, 'ended', 'pi', 'lab', ?, ?, ?, 'full', ?)`, sid, i+1, (i+1)*3, fmt.Sprintf("fp-%d", i), now)
		for e := 0; e < 4; e++ {
			// native_id/parent_id are ALTER-added columns; writing them proves
			// the fixture has the widened live schema, and the migration must
			// carry the values across.
			exec(`INSERT INTO trajectory_events (session_id, idx, kind, text, ts, native_id, parent_id)
			      VALUES (?, ?, 'user', ?, ?, ?, ?)`,
				sid, e, fmt.Sprintf("event %d text with 'quotes' and, commas", e), now,
				fmt.Sprintf("nid-%s-%d", sid, e), fmt.Sprintf("pid-%s-%d", sid, e))
		}
		exec(`INSERT INTO extraction_state (session_id, stage, processed_turn_count, processed_at, status)
		      VALUES (?, 'facts', ?, ?, 'ok')`, sid, i+1, now)
		exec(`INSERT INTO episode_memories (session_id, task, outcome, model, created_at)
		      VALUES (?, ?, 'success', 'test-model', ?)`, sid, "task for "+sid, now)
		exec(`INSERT INTO raw_sources (session_id, path, kind, native_session_id, bytes, captured_at)
		      VALUES (?, ?, 'pi-jsonl', ?, 4096, ?)`, sid, "/traj/"+sid+".jsonl", "native-"+sid, now)
	}
	// Session-independent memory tables.
	for i := 0; i < 5; i++ {
		exec(`INSERT INTO semantic_memories (type, statement, first_seen_at, last_seen_at, updated_at, canonical_of_json, agent_rationale, verify_verdict)
		      VALUES ('fact', ?, ?, ?, ?, '[1,2]', 'because', 'verified')`,
			fmt.Sprintf("statement %d", i), now, now, now)
		exec(`INSERT INTO skill_candidates (target_skill_name, title, body_md, created_at, updated_at, curator_reason, verify_verdict)
		      VALUES (?, ?, '# body', ?, ?, 'useful', 'ok')`,
			fmt.Sprintf("skill-%d", i), fmt.Sprintf("Skill %d", i), now, now)
	}
	for i := 0; i < 2; i++ {
		exec(`INSERT INTO evolution_runs (started_at, digest, notes) VALUES (?, ?, ?)`,
			now, fmt.Sprintf("digest-%d", i), "note")
	}
	for i := 0; i < 6; i++ {
		exec(`INSERT INTO agent_calls (run_id, node, ref, ok, created_at) VALUES (?, 'reflect', ?, 1, ?)`,
			(i%2)+1, fmt.Sprintf("ref-%d", i), now)
	}
}

// ---- assertion helpers ----

func tableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[n] = true
	}
	return out
}

func rowCount(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT count(*) FROM "` + table + `"`).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// snapshotCounts records row counts for the given tables.
func snapshotCounts(t *testing.T, path string, tables []string) map[string]int64 {
	t.Helper()
	db := openRaw(t, path)
	defer db.Close()
	out := map[string]int64{}
	for _, tbl := range tables {
		out[tbl] = rowCount(t, db, tbl)
	}
	return out
}

// tableDDL returns the stored CREATE TABLE text from sqlite_master.
func tableDDL(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var ddl sql.NullString
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		t.Fatalf("ddl %s: %v", table, err)
	}
	return ddl.String
}

// digest returns a stable, order-independent fingerprint of a table's full
// contents. Used to prove a round trip is byte-faithful, not merely
// count-equivalent.
func digest(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	cols := columnNames(t, db, table)
	quoted := make([]byte, 0, 64)
	for i, c := range cols {
		if i > 0 {
			quoted = append(quoted, ",'|',"...)
		}
		quoted = append(quoted, `ifnull(cast("`+c+`" as text),'<null>')`...)
	}
	// group_concat over an ordered subquery gives a deterministic digest.
	q := fmt.Sprintf(`SELECT ifnull(group_concat(rowline, char(10)), '') FROM (
		SELECT %s AS rowline FROM "%s" ORDER BY rowline)`, string(quoted), table)
	var out string
	if err := db.QueryRow(q).Scan(&out); err != nil {
		t.Fatalf("digest %s: %v", table, err)
	}
	return out
}

func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		t.Fatalf("table %s has no columns", table)
	}
	return out
}
