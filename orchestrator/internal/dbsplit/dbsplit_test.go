package dbsplit_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/dbsplit"
)

// run executes a migration against the fixture, echoing the operator-facing
// output into the test log so `go test -v` doubles as the rehearsal transcript.
func run(t *testing.T, label string, opts dbsplit.Options) (dbsplit.Result, error) {
	t.Helper()
	var buf strings.Builder
	opts.Out = &buf
	res, err := dbsplit.Run(context.Background(), opts)
	t.Logf("\n----- %s -----\n%s", label, buf.String())
	if err != nil {
		t.Logf("----- %s returned error: %v", label, err)
	}
	return res, err
}

func forward(control, memory string) dbsplit.Options {
	return dbsplit.Options{ControlPath: control, MemoryPath: memory, Direction: dbsplit.Forward}
}

func reverse(control, memory string) dbsplit.Options {
	return dbsplit.Options{ControlPath: control, MemoryPath: memory, Direction: dbsplit.Reverse}
}

// TestForwardMigration is rehearsal (a): forward migrate, verify counts, the 9
// tables are gone from orchestrator.db and present in memory.db, control tables
// untouched.
func TestForwardMigration(t *testing.T) {
	control, memory := newFixture(t)
	before := snapshotCounts(t, control, append(append([]string{}, dbsplit.MemoryTables...), controlTables...))
	beforeDigests := map[string]string{}
	func() {
		db := openRaw(t, control)
		defer db.Close()
		for _, tbl := range dbsplit.MemoryTables {
			beforeDigests[tbl] = digest(t, db, tbl)
		}
	}()

	res, err := run(t, "forward migrate", forward(control, memory))
	if err != nil {
		t.Fatalf("forward migration failed: %v", err)
	}
	if len(res.Copied) != len(dbsplit.MemoryTables) {
		t.Errorf("copied %d tables, want %d", len(res.Copied), len(dbsplit.MemoryTables))
	}
	if len(res.Dropped) != len(dbsplit.MemoryTables) {
		t.Errorf("dropped %d tables, want %d", len(res.Dropped), len(dbsplit.MemoryTables))
	}
	if !res.Vacuumed {
		t.Errorf("expected VACUUM to have run")
	}

	controlDB := openRaw(t, control)
	defer controlDB.Close()
	memDB := openRaw(t, memory)
	defer memDB.Close()

	controlTablesNow := tableNames(t, controlDB)
	memTablesNow := tableNames(t, memDB)
	for _, tbl := range dbsplit.MemoryTables {
		if controlTablesNow[tbl] {
			t.Errorf("%s still present in orchestrator.db", tbl)
		}
		if !memTablesNow[tbl] {
			t.Fatalf("%s missing from memory.db", tbl)
		}
		if got := rowCount(t, memDB, tbl); got != before[tbl] {
			t.Errorf("%s: memory.db has %d rows, orchestrator.db had %d", tbl, got, before[tbl])
		}
		// Full-content digest, not just counts: catches a lost column or a
		// mangled value that a count check would miss.
		if got := digest(t, memDB, tbl); got != beforeDigests[tbl] {
			t.Errorf("%s: content digest changed across the copy", tbl)
		}
	}

	// Control tables untouched, including the FTS-backed messages table.
	for _, tbl := range controlTables {
		if !controlTablesNow[tbl] {
			t.Errorf("control table %s vanished from orchestrator.db", tbl)
		}
		if got := rowCount(t, controlDB, tbl); got != before[tbl] {
			t.Errorf("control table %s: %d rows after, %d before", tbl, got, before[tbl])
		}
		if memTablesNow[tbl] {
			t.Errorf("control table %s must NOT have been copied into memory.db", tbl)
		}
	}
	// FTS still functions after the VACUUM.
	var ftsHits int
	if err := controlDB.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'batteries'`).Scan(&ftsHits); err != nil {
		t.Fatalf("messages_fts query after migration: %v", err)
	}
	if ftsHits == 0 {
		t.Errorf("messages_fts returned no hits after migration; the index was disturbed")
	}
	t.Logf("control tables intact; messages_fts MATCH 'batteries' -> %d hits", ftsHits)

	// The 5 sessions(id) FKs must be gone on the memory side (cross-file FKs
	// are impossible), while other constraints survive.
	for _, tbl := range []string{"trajectory_snapshots", "trajectory_events", "extraction_state", "episode_memories", "raw_sources"} {
		ddl := tableDDL(t, memDB, tbl)
		if strings.Contains(strings.ToUpper(ddl), "REFERENCES SESSIONS") {
			t.Errorf("%s in memory.db still references sessions:\n%s", tbl, ddl)
		}
	}
	// Constraints other than the FK must be intact: PK, CHECK, DEFAULT.
	eventsDDL := tableDDL(t, memDB, "trajectory_events")
	for _, want := range []string{"PRIMARY KEY(session_id, idx)", "CHECK(kind IN", "native_id", "parent_id"} {
		if !strings.Contains(eventsDDL, want) {
			t.Errorf("trajectory_events DDL in memory.db lost %q:\n%s", want, eventsDDL)
		}
	}
	// A CHECK constraint must still reject bad data on the memory side.
	if _, err := memDB.Exec(`INSERT INTO trajectory_events (session_id, idx, kind, text) VALUES ('x', 99, 'bogus-kind', 't')`); err == nil {
		t.Errorf("expected CHECK(kind IN ...) to reject 'bogus-kind' in memory.db")
	} else {
		t.Logf("memory.db CHECK constraint still enforced: %v", err)
	}
	// The PK must still reject a duplicate.
	if _, err := memDB.Exec(`INSERT INTO trajectory_events (session_id, idx, kind, text) VALUES ('sess-alpha', 0, 'user', 'dupe')`); err == nil {
		t.Errorf("expected PRIMARY KEY(session_id, idx) to reject a duplicate in memory.db")
	} else {
		t.Logf("memory.db PRIMARY KEY still enforced: %v", err)
	}
	// Indexes carried across.
	var idxCount int
	if err := memDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL`).Scan(&idxCount); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if idxCount == 0 {
		t.Errorf("no explicit indexes were created in memory.db")
	}
	t.Logf("memory.db carries %d explicit index(es)", idxCount)
}

// TestReverseMigration is rehearsal (b): after a forward migration, roll back
// and confirm the original state is restored, the 5 FK constraints are back in
// the sqlite_master DDL text, and FK enforcement actually works again.
func TestReverseMigration(t *testing.T) {
	control, memory := newFixture(t)

	originalDDL := map[string]string{}
	originalDigest := map[string]string{}
	originalCounts := snapshotCounts(t, control, dbsplit.MemoryTables)
	func() {
		db := openRaw(t, control)
		defer db.Close()
		for _, tbl := range dbsplit.MemoryTables {
			originalDDL[tbl] = tableDDL(t, db, tbl)
			originalDigest[tbl] = digest(t, db, tbl)
		}
	}()

	if _, err := run(t, "forward migrate (setup for rollback)", forward(control, memory)); err != nil {
		t.Fatalf("forward: %v", err)
	}

	res, err := run(t, "REVERSE migrate (rollback)", reverse(control, memory))
	if err != nil {
		t.Fatalf("reverse migration failed: %v", err)
	}
	if len(res.Copied) != len(dbsplit.MemoryTables) || len(res.Dropped) != len(dbsplit.MemoryTables) {
		t.Errorf("reverse copied %d dropped %d, want %d each", len(res.Copied), len(res.Dropped), len(dbsplit.MemoryTables))
	}
	if res.FKViolations != 0 {
		t.Errorf("reverse left %d FK violations, want 0", res.FKViolations)
	}

	controlDB := openRaw(t, control)
	defer controlDB.Close()
	memDB := openRaw(t, memory)
	defer memDB.Close()

	memNow := tableNames(t, memDB)
	controlNow := tableNames(t, controlDB)
	for _, tbl := range dbsplit.MemoryTables {
		if memNow[tbl] {
			t.Errorf("%s still present in memory.db after rollback", tbl)
		}
		if !controlNow[tbl] {
			t.Fatalf("%s missing from orchestrator.db after rollback", tbl)
		}
		if got := rowCount(t, controlDB, tbl); got != originalCounts[tbl] {
			t.Errorf("%s: %d rows after rollback, %d originally", tbl, got, originalCounts[tbl])
		}
		if got := digest(t, controlDB, tbl); got != originalDigest[tbl] {
			t.Errorf("%s: content digest differs from the pre-migration original", tbl)
		}
		// Exact pre-migration DDL, restored from the ledger's verbatim record.
		if got := tableDDL(t, controlDB, tbl); got != originalDDL[tbl] {
			t.Errorf("%s DDL not restored exactly.\n--- want ---\n%s\n--- got ---\n%s", tbl, originalDDL[tbl], got)
		}
	}

	// The 5 FK clauses are back in the DDL text.
	fkTables := []string{"trajectory_snapshots", "trajectory_events", "extraction_state", "episode_memories", "raw_sources"}
	for _, tbl := range fkTables {
		ddl := tableDDL(t, controlDB, tbl)
		if !strings.Contains(ddl, "FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE") {
			t.Errorf("%s is missing the restored sessions(id) FK:\n%s", tbl, ddl)
		}
	}
	t.Logf("all %d sessions(id) FK clauses present in restored sqlite_master DDL", len(fkTables))

	// FK enforcement actually works again: an orphan insert must be rejected.
	for _, tbl := range fkTables {
		var stmt string
		switch tbl {
		case "trajectory_snapshots":
			stmt = `INSERT INTO trajectory_snapshots (session_id, status, agent, created_at) VALUES ('no-such-session','ended','pi','now')`
		case "trajectory_events":
			stmt = `INSERT INTO trajectory_events (session_id, idx, kind, text) VALUES ('no-such-session', 0, 'user', 't')`
		case "extraction_state":
			stmt = `INSERT INTO extraction_state (session_id, stage) VALUES ('no-such-session','facts')`
		case "episode_memories":
			stmt = `INSERT INTO episode_memories (session_id, created_at) VALUES ('no-such-session','now')`
		case "raw_sources":
			stmt = `INSERT INTO raw_sources (session_id, path, kind, captured_at) VALUES ('no-such-session','/p','pi-jsonl','now')`
		}
		if _, err := controlDB.Exec(stmt); err == nil {
			t.Errorf("%s: orphan insert was ACCEPTED; FK enforcement is not working", tbl)
		} else {
			t.Logf("%s: orphan insert correctly rejected (%v)", tbl, err)
		}
	}

	// And the CASCADE still fires: deleting a session removes its memory rows.
	beforeCascade := rowCount(t, controlDB, "trajectory_events")
	if _, err := controlDB.Exec(`DELETE FROM sessions WHERE id = 'sess-alpha'`); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	afterCascade := rowCount(t, controlDB, "trajectory_events")
	if afterCascade >= beforeCascade {
		t.Errorf("ON DELETE CASCADE did not fire: trajectory_events %d -> %d", beforeCascade, afterCascade)
	}
	t.Logf("ON DELETE CASCADE verified: trajectory_events %d -> %d rows after deleting sess-alpha", beforeCascade, afterCascade)
}

// TestForwardTwiceIsNoOp is rehearsal (c) and (e): running forward twice in a
// row leaves the second run a clean no-op, which is also the
// already-fully-migrated case.
func TestForwardTwiceIsNoOp(t *testing.T) {
	control, memory := newFixture(t)
	counts := snapshotCounts(t, control, dbsplit.MemoryTables)

	if _, err := run(t, "forward migrate (run 1)", forward(control, memory)); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	res, err := run(t, "forward migrate (run 2 - expect clean no-op)", forward(control, memory))
	if err != nil {
		t.Fatalf("run 2 must not error on an already-migrated database: %v", err)
	}
	if !res.NoWorkNeeded() {
		t.Errorf("run 2 did work (copied=%v dropped=%v); expected a no-op", res.Copied, res.Dropped)
	}
	if len(res.AlreadyDone) != len(dbsplit.MemoryTables) {
		t.Errorf("run 2 recognised %d already-migrated tables, want %d", len(res.AlreadyDone), len(dbsplit.MemoryTables))
	}

	// No row duplication from the second run.
	memDB := openRaw(t, memory)
	defer memDB.Close()
	for _, tbl := range dbsplit.MemoryTables {
		if got := rowCount(t, memDB, tbl); got != counts[tbl] {
			t.Errorf("%s: %d rows after the second run, want %d (duplication)", tbl, got, counts[tbl])
		}
	}

	// A dry run against the already-migrated state must also be clean.
	dry := forward(control, memory)
	dry.DryRun = true
	if _, err := run(t, "forward --dry-run against already-migrated state", dry); err != nil {
		t.Errorf("dry run on migrated state: %v", err)
	}
}

// TestResumeAfterAbort is rehearsal (d): abort the migration partway (simulating
// a kill after 6 of the 9 tables were copied) and re-run; it must complete
// correctly with no duplicate rows.
//
// The abort point is deterministic via the OnTableCopied hook, which fires after
// each copy transaction commits — exactly the state a SIGKILL between tables
// would leave behind.
func TestResumeAfterAbort(t *testing.T) {
	control, memory := newFixture(t)
	counts := snapshotCounts(t, control, dbsplit.MemoryTables)

	const abortAfter = 6
	opts := forward(control, memory)
	copiedSoFar := 0
	opts.OnTableCopied = func(string) error {
		copiedSoFar++
		if copiedSoFar >= abortAfter {
			return fmt.Errorf("simulated kill -9 after %d tables", abortAfter)
		}
		return nil
	}
	res, err := run(t, fmt.Sprintf("forward migrate, ABORTED after %d tables", abortAfter), opts)
	if err == nil {
		t.Fatalf("expected the simulated abort to surface as an error")
	}
	t.Logf("aborted as intended: %v", err)
	if len(res.Dropped) != 0 {
		t.Fatalf("abort left %d dropped tables; the drop phase must be gated behind verification", len(res.Dropped))
	}

	// Inspect the wreckage: 6 tables copied, none dropped (the drop phase is
	// gated behind full verification, which never ran).
	memDB := openRaw(t, memory)
	present := tableNames(t, memDB)
	copied := 0
	for _, tbl := range dbsplit.MemoryTables {
		if present[tbl] {
			copied++
		}
	}
	memDB.Close()
	t.Logf("post-abort state: %d/%d tables copied into memory.db, 0 dropped from orchestrator.db",
		copied, len(dbsplit.MemoryTables))
	if copied != abortAfter {
		t.Fatalf("expected exactly %d tables copied, found %d", abortAfter, copied)
	}
	// The source is still fully intact at this point.
	func() {
		db := openRaw(t, control)
		defer db.Close()
		for _, tbl := range dbsplit.MemoryTables {
			if !tableNames(t, db)[tbl] {
				t.Errorf("%s missing from orchestrator.db after the abort; no data should have been destroyed", tbl)
			}
		}
	}()

	// Re-run to completion.
	res, err = run(t, "forward migrate (RESUME after abort)", forward(control, memory))
	if err != nil {
		t.Fatalf("resume run failed: %v", err)
	}
	t.Logf("resume: copied=%d dropped=%d already_done=%d", len(res.Copied), len(res.Dropped), len(res.AlreadyDone))

	controlDB := openRaw(t, control)
	defer controlDB.Close()
	memDB2 := openRaw(t, memory)
	defer memDB2.Close()
	controlNow := tableNames(t, controlDB)
	for _, tbl := range dbsplit.MemoryTables {
		if controlNow[tbl] {
			t.Errorf("%s still in orchestrator.db after resume", tbl)
		}
		// The critical assertion: exactly the original row count, no doubling
		// from the table being copied twice.
		if got := rowCount(t, memDB2, tbl); got != counts[tbl] {
			t.Errorf("%s: %d rows after resume, want %d (duplicate insert)", tbl, got, counts[tbl])
		}
	}
	t.Logf("resume complete: all %d tables migrated with no duplicated rows", len(dbsplit.MemoryTables))
}

// TestVerificationRefusesToDropOnMismatch is rehearsal (f): a deliberately
// corrupted destination count must block the drop, leaving the source intact.
func TestVerificationRefusesToDropOnMismatch(t *testing.T) {
	control, memory := newFixture(t)
	counts := snapshotCounts(t, control, dbsplit.MemoryTables)

	// Stage the "copied but not yet dropped" state by aborting right after the
	// last copy commits, so the next run enters the verify gate for all 9
	// tables with the source still present.
	opts := forward(control, memory)
	copied := 0
	opts.OnTableCopied = func(string) error {
		copied++
		if copied == len(dbsplit.MemoryTables) {
			return fmt.Errorf("stage: stop before the drop phase")
		}
		return nil
	}
	if _, err := run(t, "forward, stopped after all copies (pre-drop state)", opts); err == nil {
		t.Fatalf("staging abort did not fire")
	}

	memDB := openRaw(t, memory)
	staged := 0
	for _, tbl := range dbsplit.MemoryTables {
		if tableNames(t, memDB)[tbl] {
			staged++
		}
	}
	if staged != len(dbsplit.MemoryTables) {
		t.Fatalf("staged %d/%d tables", staged, len(dbsplit.MemoryTables))
	}
	// Deliberately corrupt a count in the destination.
	if _, err := memDB.Exec(`DELETE FROM agent_calls WHERE rowid = (SELECT min(rowid) FROM agent_calls)`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	tamperedCount := rowCount(t, memDB, "agent_calls")
	memDB.Close()
	t.Logf("deliberately deleted 1 row from memory.db agent_calls (now %d, source has %d)",
		tamperedCount, counts["agent_calls"])

	res, err := run(t, "forward migrate with a CORRUPTED destination count (expect refusal)", forward(control, memory))
	if err == nil {
		t.Fatalf("expected verification to fail and refuse the drop, got success: %+v", res)
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("expected a verification error, got: %v", err)
	}
	if len(res.Dropped) != 0 {
		t.Errorf("tables were dropped despite failed verification: %v", res.Dropped)
	}
	t.Logf("refused as required: %v", err)

	// The source must be completely intact — nothing destroyed.
	controlDB := openRaw(t, control)
	defer controlDB.Close()
	controlNow := tableNames(t, controlDB)
	for _, tbl := range dbsplit.MemoryTables {
		if !controlNow[tbl] {
			t.Fatalf("%s was dropped from orchestrator.db despite failed verification", tbl)
		}
		if got := rowCount(t, controlDB, tbl); got != counts[tbl] {
			t.Errorf("%s: source row count changed to %d (want %d)", tbl, got, counts[tbl])
		}
	}
	t.Logf("source orchestrator.db fully intact: all %d tables present with original counts", len(dbsplit.MemoryTables))
}

// TestSchemaMismatchRefusesToDrop covers the case a row-count check alone cannot
// catch: a destination table that is missing a column.
func TestSchemaMismatchRefusesToDrop(t *testing.T) {
	control, memory := newFixture(t)

	// Pre-create evolution_runs in memory.db WITHOUT the notes column, then run
	// forward. The copy step skips it (already present in dest), so verification
	// is what has to catch the missing column.
	memDB := openRaw(t, memory)
	if _, err := memDB.Exec(`CREATE TABLE evolution_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		started_at TEXT NOT NULL,
		finished_at TEXT,
		phase0_plan_json TEXT NOT NULL DEFAULT '{}',
		counts_json TEXT NOT NULL DEFAULT '{}',
		digest TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("stage truncated table: %v", err)
	}
	// Match the source row count exactly, so ONLY the schema check can catch it.
	srcCount := snapshotCounts(t, control, []string{"evolution_runs"})["evolution_runs"]
	for i := int64(0); i < srcCount; i++ {
		if _, err := memDB.Exec(`INSERT INTO evolution_runs (started_at) VALUES ('now')`); err != nil {
			t.Fatalf("stage rows: %v", err)
		}
	}
	memDB.Close()
	t.Logf("staged memory.db evolution_runs missing the 'notes' column, with a MATCHING row count of %d", srcCount)

	res, err := run(t, "forward migrate with a column missing in the destination (expect refusal)", forward(control, memory))
	if err == nil {
		t.Fatalf("expected the schema check to refuse the drop, got success: %+v", res)
	}
	if !strings.Contains(err.Error(), "verification failed") || !strings.Contains(err.Error(), "column count") {
		t.Errorf("expected a column-count verification error, got: %v", err)
	}
	if len(res.Dropped) != 0 {
		t.Errorf("dropped tables despite the schema mismatch: %v", res.Dropped)
	}
	t.Logf("refused as required: %v", err)

	controlDB := openRaw(t, control)
	defer controlDB.Close()
	if !tableNames(t, controlDB)["evolution_runs"] {
		t.Errorf("evolution_runs was dropped from orchestrator.db despite failed verification")
	}
}

// TestDryRunChangesNothing asserts --dry-run is inert.
func TestDryRunChangesNothing(t *testing.T) {
	control, memory := newFixture(t)
	before := snapshotCounts(t, control, dbsplit.MemoryTables)

	opts := forward(control, memory)
	opts.DryRun = true
	res, err := run(t, "forward --dry-run", opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(res.Copied) != 0 || len(res.Dropped) != 0 {
		t.Errorf("dry run reported work done: copied=%v dropped=%v", res.Copied, res.Dropped)
	}
	if _, err := os.Stat(memory); err == nil {
		t.Errorf("dry run created memory.db")
	}
	after := snapshotCounts(t, control, dbsplit.MemoryTables)
	for tbl, n := range before {
		if after[tbl] != n {
			t.Errorf("%s: dry run changed row count %d -> %d", tbl, n, after[tbl])
		}
	}
	controlDB := openRaw(t, control)
	defer controlDB.Close()
	for _, tbl := range dbsplit.MemoryTables {
		if !tableNames(t, controlDB)[tbl] {
			t.Errorf("dry run dropped %s", tbl)
		}
	}
}

// TestRoundTripTwice exercises forward -> reverse -> forward -> reverse, which
// is the sequence an operator performs if they roll back, fix something, and try
// again. Data must survive every leg.
func TestRoundTripTwice(t *testing.T) {
	control, memory := newFixture(t)
	original := map[string]string{}
	counts := snapshotCounts(t, control, dbsplit.MemoryTables)
	func() {
		db := openRaw(t, control)
		defer db.Close()
		for _, tbl := range dbsplit.MemoryTables {
			original[tbl] = digest(t, db, tbl)
		}
	}()

	for round := 1; round <= 2; round++ {
		if _, err := run(t, fmt.Sprintf("round %d: forward", round), forward(control, memory)); err != nil {
			t.Fatalf("round %d forward: %v", round, err)
		}
		if _, err := run(t, fmt.Sprintf("round %d: reverse", round), reverse(control, memory)); err != nil {
			t.Fatalf("round %d reverse: %v", round, err)
		}
		db := openRaw(t, control)
		for _, tbl := range dbsplit.MemoryTables {
			if got := rowCount(t, db, tbl); got != counts[tbl] {
				t.Errorf("round %d: %s has %d rows, want %d", round, tbl, got, counts[tbl])
			}
			if got := digest(t, db, tbl); got != original[tbl] {
				t.Errorf("round %d: %s content changed", round, tbl)
			}
		}
		db.Close()
	}
	t.Logf("two full forward/reverse round trips preserved all data byte-for-byte")
}

// TestReverseOnPristineDatabaseIsNoOp covers the operator mistake of running the
// rollback when no forward migration ever happened.
//
// This is correctly a clean no-op rather than an error: the 9 tables are already
// present in orchestrator.db and absent from memory.db, which is exactly the
// end state the reverse migration exists to produce. Erroring here would mean an
// operator who is unsure whether the rollback already ran cannot safely find out
// by running it.
func TestReverseOnPristineDatabaseIsNoOp(t *testing.T) {
	control, memory := newFixture(t)
	counts := snapshotCounts(t, control, dbsplit.MemoryTables)

	res, err := run(t, "reverse migrate on a never-split database", reverse(control, memory))
	if err != nil {
		t.Fatalf("reverse on a pristine database should be a safe no-op, got: %v", err)
	}
	if !res.NoWorkNeeded() {
		t.Errorf("expected a no-op, got copied=%v dropped=%v", res.Copied, res.Dropped)
	}
	// Nothing touched.
	after := snapshotCounts(t, control, dbsplit.MemoryTables)
	for tbl, n := range counts {
		if after[tbl] != n {
			t.Errorf("%s: row count changed %d -> %d", tbl, n, after[tbl])
		}
	}
	t.Logf("no-op confirmed; all %d tables left in orchestrator.db untouched", len(dbsplit.MemoryTables))
}

// TestMissingFromBothFilesRefuses covers the genuinely broken state: a table
// absent from both files. That means data loss has already happened, and the
// tool must refuse rather than paper over it.
func TestMissingFromBothFilesRefuses(t *testing.T) {
	control, memory := newFixture(t)

	db := openRaw(t, control)
	if _, err := db.Exec(`DROP TABLE agent_calls`); err != nil {
		t.Fatalf("stage missing table: %v", err)
	}
	db.Close()
	t.Logf("dropped agent_calls from orchestrator.db without copying it anywhere")

	_, err := run(t, "forward migrate with agent_calls missing from BOTH files (expect refusal)", forward(control, memory))
	if err == nil {
		t.Fatalf("expected a refusal when a table is missing from both files")
	}
	if !strings.Contains(err.Error(), "missing from BOTH") {
		t.Errorf("expected a 'missing from BOTH' message, got: %v", err)
	}
	t.Logf("refused with a clear operator message: %v", err)
}
