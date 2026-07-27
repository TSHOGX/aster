package memorystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// controlTablesExpectedInControlDB lists control-plane tables that must NOT
// appear in memory.db. It is a representative set covering each control table
// group (identity, session lifecycle, messaging, runtime, resources,
// notifications) rather than all 25, so the test states an invariant instead of
// re-listing a schema that legitimately grows.
var controlTablesExpectedInControlDB = []string{
	"users",
	"auth_sessions",
	"sessions",
	"messages",
	"messages_fts",
	"message_traces",
	"message_feedback",
	"turns",
	"events",
	"artifacts",
	"runtime_generations",
	"runtime_generation_resources",
	"runtime_resource_instances",
	"session_driver_homes",
	"session_driver_states",
	"notifications",
}

// listTables reads every table name out of one database's OWN sqlite_master.
// Internal sqlite_* objects are skipped; FTS5 shadow tables (messages_fts_data
// and friends) are collapsed away so the comparison is over declared tables.
func listTables(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
SELECT name FROM sqlite_master
WHERE type IN ('table','view')
  AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		// FTS5 creates messages_fts_{data,idx,content,docsize,config}; they are
		// implementation detail of the messages_fts virtual table.
		if strings.HasPrefix(name, "messages_fts_") {
			continue
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return out
}

// timeZero is the zero time, spelled as a helper so the calls above read as
// "unset" rather than as a magic literal.
func timeZero() time.Time { return time.Time{} }

// TestDatabasesShareNoTables is the structural half of the split: query
// sqlite_master on each file and assert neither side can see the other's tables.
// A regression that re-adds a memory table to the control schema (or vice versa)
// fails here rather than silently reintroducing the shared write lock.
func TestDatabasesShareNoTables(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	control, err := store.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })
	mem, err := Open(ctx, filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	controlTables := listTables(t, ctx, control.DBForTest())
	memoryTables := listTables(t, ctx, mem.DBForTest())

	inSet := func(names []string, want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}

	// 1. Every memory table is in memory.db and in NO control table list.
	for _, want := range MemoryTables {
		if !inSet(memoryTables, want) {
			t.Errorf("memory.db is missing memory table %q (have %v)", want, memoryTables)
		}
		if inSet(controlTables, want) {
			t.Errorf("control.db still declares memory table %q: the split leaked", want)
		}
	}

	// 2. No control table appears in memory.db.
	for _, unwanted := range controlTablesExpectedInControlDB {
		if !inSet(controlTables, unwanted) {
			t.Errorf("control.db is missing expected control table %q (test list is stale)", unwanted)
		}
		if inSet(memoryTables, unwanted) {
			t.Errorf("memory.db declares control table %q: the split leaked", unwanted)
		}
	}

	// 3. The two table sets are fully disjoint, which catches a table added to
	// both schemas that neither list above happens to name.
	overlap := []string{}
	for _, m := range memoryTables {
		if inSet(controlTables, m) {
			overlap = append(overlap, m)
		}
	}
	sort.Strings(overlap)
	if len(overlap) != 0 {
		t.Errorf("control.db and memory.db share tables %v; the two schemas must be disjoint", overlap)
	}
}

// TestMemoryWritesSucceedWithControlDBClosed is the behavioral half: it proves
// the databases are genuinely independent rather than merely separately declared.
// The control store is CLOSED — its connections are gone and its file is
// unreferenced — and the full set of memory writes still has to succeed. Under
// the old shared-database arrangement every one of these would fail.
func TestMemoryWritesSucceedWithControlDBClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	control, err := store.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	mem, err := Open(ctx, filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	// Close the control plane entirely.
	if err := control.Close(); err != nil {
		t.Fatalf("close control store: %v", err)
	}

	const sessionID = "sess_no_control"

	// Rebuildable half: an event-stream replace spanning more than one chunk,
	// plus its index row. Note session_id references a session that this process
	// can no longer even look up — exactly the unenforced soft key the schema
	// comment describes.
	events := make([]TrajectoryEvent, trajectoryEventBatchSize+7)
	for i := range events {
		events[i] = TrajectoryEvent{SessionID: sessionID, Idx: i, Kind: "user", Text: "e"}
	}
	if err := mem.ReplaceTrajectoryEvents(ctx, TrajectorySnapshot{
		SessionID: sessionID, Status: "created", Agent: "pi", UserID: "lab",
		MessageCount: len(events), Fingerprint: "fp", Fidelity: "full",
	}, events); err != nil {
		t.Fatalf("replace trajectory events with control db closed: %v", err)
	}
	got, err := mem.GetTrajectoryEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("get trajectory events with control db closed: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("read back %d events, want %d", len(got), len(events))
	}

	// Non-rebuildable half: one write into each distilled table.
	if _, err := mem.InsertEpisodeMemory(ctx, EpisodeMemory{
		SessionID: sessionID, Task: "independent write", Outcome: "success",
	}); err != nil {
		t.Fatalf("insert episode memory with control db closed: %v", err)
	}
	if _, err := mem.UpsertSemanticMemory(ctx, SemanticMemory{
		Type: "fact", Scope: "global", Statement: "the two databases are independent",
	}, sessionID); err != nil {
		t.Fatalf("upsert semantic memory with control db closed: %v", err)
	}
	if _, err := mem.UpsertSkillCandidate(ctx, SkillCandidate{
		TargetSkillName: "independent-writes", Title: "Independent writes", BodyMD: "# body",
	}); err != nil {
		t.Fatalf("upsert skill candidate with control db closed: %v", err)
	}
	if err := mem.SetExtractionState(ctx, ExtractionState{
		SessionID: sessionID, Stage: "facts", ProcessedTurnCount: 1, Status: "ok",
	}); err != nil {
		t.Fatalf("set extraction state with control db closed: %v", err)
	}
	if err := mem.UpsertRawSource(ctx, sessionID, "/tmp/x.jsonl", "pi_jsonl", "native-1", 10, timeZero()); err != nil {
		t.Fatalf("upsert raw source with control db closed: %v", err)
	}
	runID, err := mem.StartEvolutionRun(ctx, "{}")
	if err != nil {
		t.Fatalf("start evolution run with control db closed: %v", err)
	}
	if err := mem.RecordAgentCall(ctx, AgentCall{RunID: runID, Node: "reflect", OK: true}); err != nil {
		t.Fatalf("record agent call with control db closed: %v", err)
	}
	if err := mem.FinishEvolutionRun(ctx, runID, "{}", "digest", "notes"); err != nil {
		t.Fatalf("finish evolution run with control db closed: %v", err)
	}

	// And the reads used by the pipeline still work.
	if snaps, err := mem.ListSessionsForDistillation(ctx, timeZero()); err != nil {
		t.Fatalf("list sessions for distillation with control db closed: %v", err)
	} else if len(snaps) != 1 || snaps[0].SessionID != sessionID {
		t.Fatalf("expected one snapshot for %q, got %+v", sessionID, snaps)
	}
}
