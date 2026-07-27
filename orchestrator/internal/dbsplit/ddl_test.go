package dbsplit

import "strings"

import "testing"

// TestStripSessionFKsPreservesOtherConstraints guards the riskiest piece of
// string surgery in the package: removing the sessions(id) FK clause without
// disturbing a CHECK constraint whose body contains commas and quoted strings.
func TestStripSessionFKsPreservesOtherConstraints(t *testing.T) {
	// The live trajectory_events DDL, post-ALTER, as read from sqlite_master.
	ddl := `CREATE TABLE trajectory_events (
  session_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('user','assistant','thinking','tool_call','tool_result','system','usage')),
  subtype TEXT,
  is_error INTEGER NOT NULL DEFAULT 0 CHECK(is_error IN (0,1)),
  text TEXT NOT NULL,
  ts TEXT, native_id TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(session_id, idx),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
)`
	out, removed, err := stripSessionFKs(ddl, "mem")
	if err != nil {
		t.Fatalf("stripSessionFKs: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %d clauses, want 1: %v", len(removed), removed)
	}
	if strings.Contains(strings.ToUpper(out), "REFERENCES SESSIONS") {
		t.Errorf("FK not removed:\n%s", out)
	}
	// Everything else must survive, including the comma-bearing CHECK bodies and
	// the ALTER-added trailing columns.
	for _, want := range []string{
		`CHECK(kind IN ('user','assistant','thinking','tool_call','tool_result','system','usage'))`,
		`CHECK(is_error IN (0,1))`,
		`PRIMARY KEY(session_id, idx)`,
		`native_id TEXT NOT NULL DEFAULT ''`,
		`parent_id TEXT NOT NULL DEFAULT ''`,
		`"mem"."trajectory_events"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost %q:\n%s", want, out)
		}
	}
}

// TestStripSessionFKsRejectsUnknownFK asserts we refuse to silently drop a
// foreign key we were not told about, rather than quietly losing a constraint.
func TestStripSessionFKsRejectsUnknownFK(t *testing.T) {
	ddl := `CREATE TABLE x (
  a TEXT,
  b TEXT,
  FOREIGN KEY(b) REFERENCES some_other_table(id) ON DELETE CASCADE
)`
	if _, _, err := stripSessionFKs(ddl, "mem"); err == nil {
		t.Fatalf("expected a refusal for an unexpected FK target")
	} else if !strings.Contains(err.Error(), "re-audit") {
		t.Errorf("expected a re-audit message, got: %v", err)
	}
}

// TestStripSessionFKsRejectsInlineReferences covers the column-level REFERENCES
// form, which our audited set does not use; encountering it means the schema
// moved and the operator should re-audit.
func TestStripSessionFKsRejectsInlineReferences(t *testing.T) {
	ddl := `CREATE TABLE x (
  a TEXT,
  session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE
)`
	if _, _, err := stripSessionFKs(ddl, "mem"); err == nil {
		t.Fatalf("expected a refusal for an inline REFERENCES")
	}
}

// TestInlineReferencesNotConfusedByCheckBody makes sure a CHECK constraint or a
// DEFAULT string literal mentioning the word "references" is not mistaken for an
// inline foreign key.
func TestInlineReferencesNotConfusedByCheckBody(t *testing.T) {
	ddl := `CREATE TABLE x (
  a TEXT NOT NULL DEFAULT 'references sessions(id)',
  b TEXT CHECK(b IN ('references','other')),
  PRIMARY KEY(a)
)`
	out, removed, err := stripSessionFKs(ddl, "mem")
	if err != nil {
		t.Fatalf("false positive on a quoted literal: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed %v, want nothing", removed)
	}
	if !strings.Contains(out, `DEFAULT 'references sessions(id)'`) {
		t.Errorf("literal mangled:\n%s", out)
	}
}

// TestRestoreSessionFK covers the rollback fallback used when the ledger is
// unavailable.
func TestRestoreSessionFK(t *testing.T) {
	stripped := `CREATE TABLE "mem"."raw_sources" (
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  PRIMARY KEY(session_id, path)
)`
	out, err := restoreSessionFK(stripped, "main")
	if err != nil {
		t.Fatalf("restoreSessionFK: %v", err)
	}
	if !strings.Contains(out, "FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE") {
		t.Errorf("FK not restored:\n%s", out)
	}
	if !strings.Contains(out, `PRIMARY KEY(session_id, path)`) {
		t.Errorf("PK lost:\n%s", out)
	}
	// Idempotent: restoring twice must not duplicate the clause.
	twice, err := restoreSessionFK(out, "main")
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if got := strings.Count(strings.ToUpper(twice), "FOREIGN KEY"); got != 1 {
		t.Errorf("FK clause appears %d times after a double restore:\n%s", got, twice)
	}
}

// TestSplitTopLevel checks the comma splitter against nesting and quoting.
func TestSplitTopLevel(t *testing.T) {
	parts, err := splitTopLevel(`a INT, b TEXT CHECK(b IN ('x','y')), PRIMARY KEY(a, b)`)
	if err != nil {
		t.Fatalf("splitTopLevel: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3: %q", len(parts), parts)
	}
	if !strings.Contains(parts[1], `IN ('x','y')`) {
		t.Errorf("split inside a CHECK body: %q", parts[1])
	}
	if !strings.Contains(parts[2], `PRIMARY KEY(a, b)`) {
		t.Errorf("split inside a PK list: %q", parts[2])
	}
}

// TestRequalifyCreateIndex asserts the index is redirected to the target schema
// while the ON clause and any partial-index WHERE survive verbatim.
func TestRequalifyCreateIndex(t *testing.T) {
	out, err := requalifyCreateIndex(
		`CREATE UNIQUE INDEX IF NOT EXISTS skill_candidates_name_uq
  ON skill_candidates (target_skill_name) WHERE status <> 'rejected'`, "mem")
	if err != nil {
		t.Fatalf("requalifyCreateIndex: %v", err)
	}
	if !strings.Contains(out, `"mem"."skill_candidates_name_uq"`) {
		t.Errorf("index not requalified:\n%s", out)
	}
	if !strings.Contains(out, `ON skill_candidates (target_skill_name)`) {
		t.Errorf("ON clause altered:\n%s", out)
	}
	if !strings.Contains(out, `WHERE status <> 'rejected'`) {
		t.Errorf("partial-index WHERE lost:\n%s", out)
	}
}

// TestRequalifyCreateTablePreservesSuffix covers table options after the closing
// paren (WITHOUT ROWID, STRICT), which must not be dropped.
func TestRequalifyCreateTablePreservesSuffix(t *testing.T) {
	out, err := requalifyCreateTable(`CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT) WITHOUT ROWID`, "mem")
	if err != nil {
		t.Fatalf("requalifyCreateTable: %v", err)
	}
	if !strings.HasSuffix(out, "WITHOUT ROWID") {
		t.Errorf("suffix lost:\n%s", out)
	}
	if !strings.Contains(out, `"mem"."kv"`) {
		t.Errorf("not requalified:\n%s", out)
	}
}

// TestQuotedIdentifierTableName covers a table whose name needs quoting.
func TestQuotedIdentifierTableName(t *testing.T) {
	parts, err := parseCreateTable(`CREATE TABLE "odd name" (a INT)`)
	if err != nil {
		t.Fatalf("parseCreateTable: %v", err)
	}
	if parts.Name != "odd name" {
		t.Errorf("name = %q, want %q", parts.Name, "odd name")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:             "512 B",
		2048:            "2.0 KiB",
		5 * 1024 * 1024: "5.0 MiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
