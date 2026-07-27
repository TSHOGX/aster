// Package dbsplit implements the one-window migration that moves the
// trajectory-distillation ("memory") tables out of the single orchestrator.db
// into a sibling memory.db, and the rehearsed reverse migration that moves them
// back.
//
// It operates on the database FILES via SQL only. It deliberately does not use
// the store package's Go types or its schema literals:
//
//   - The live column set is read from the target file's own sqlite_master and
//     pragma_table_info, so incremental `ALTER TABLE ... ADD COLUMN` migrations
//     already applied in production (native_id/parent_id on trajectory_events,
//     the agentic columns on semantic_memories/skill_candidates, and anything
//     added after this code was written) are carried across automatically.
//   - Being independent of the application's Go packages keeps the migration
//     verifiable on its own and immune to concurrent refactors of the store.
//
// Strategy (operator-chosen, single maintenance window, orchestrator STOPPED):
//
//  1. copy the 9 tables into memory.db via ATTACH
//  2. verify per-table row counts AND column signatures, source vs destination
//  3. DROP the 9 tables from orchestrator.db, then VACUUM to reclaim space
//  4. restart
//
// Step 3 destroys the source, so the only rollback is the reverse migration,
// which is a first-class deliverable here (Direction Reverse) and restores the
// original DDL including the 5 sessions(id) foreign keys.
package dbsplit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// MemoryTables is the authoritative, audited set of tables that live in
// memory.db after the split. Everything else stays in orchestrator.db.
//
// Order matters only for readable progress output; the tables have no
// cross-references among themselves (the two lifecycle groups documented in
// store/schema.go are joined by soft keys only), so any order is safe.
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

// ledgerTable records, inside memory.db, what the forward migration copied: the
// verbatim pre-migration DDL (including the FK clauses that had to be dropped),
// the index DDL, and the source row count observed at copy time.
//
// The ledger is an audit trail and the reverse migration's source of truth for
// exact DDL restoration. It is deliberately NOT the resumability mechanism:
// per-table completion is derived from the actual state of the two files (see
// inspect), so a lost or truncated ledger degrades the rollback to a
// reconstructed-DDL path instead of blocking it.
const ledgerTable = "_dbsplit_ledger"

// Direction selects which way data moves.
type Direction int

const (
	// Forward moves the memory tables orchestrator.db -> memory.db.
	Forward Direction = iota
	// Reverse moves them memory.db -> orchestrator.db, restoring the original
	// DDL including the sessions(id) foreign keys. This is the rollback.
	Reverse
)

func (d Direction) String() string {
	if d == Reverse {
		return "reverse"
	}
	return "forward"
}

// Options configures a migration run.
type Options struct {
	// ControlPath is orchestrator.db (the control-plane database).
	ControlPath string
	// MemoryPath is memory.db. Defaults to memory.db beside ControlPath.
	MemoryPath string
	// Direction selects forward (split out) or reverse (roll back).
	Direction Direction
	// DryRun reports the plan and the current state, changing nothing.
	DryRun bool
	// SkipVacuum leaves the source file un-VACUUMed after a verified drop.
	// The space is still free for reuse inside the file, just not returned to
	// the filesystem.
	SkipVacuum bool
	// Out receives operator-facing progress. Defaults to os.Stdout.
	Out io.Writer

	// OnTableCopied, when set, is called after each table's copy transaction
	// commits. Returning an error aborts the run immediately, before the verify
	// and drop phases. This exists so the resumability rehearsal can simulate a
	// mid-run kill deterministically instead of racing a real signal; it has no
	// production use and defaults to nil.
	OnTableCopied func(table string) error
}

// Result summarizes a completed run.
type Result struct {
	Direction Direction
	DryRun    bool
	Tables    []TableState
	// Copied/Dropped are the tables this run actually acted on.
	Copied  []string
	Dropped []string
	// AlreadyDone are tables that required no work (a prior run finished them).
	AlreadyDone []string
	Vacuumed    bool
	// FreedBytes is the source file's size reduction from VACUUM.
	FreedBytes int64
	// FKViolations counts rows failing foreign_key_check after a reverse
	// migration. Non-zero is reported loudly but does not fail the rollback.
	FKViolations int
}

// NoWorkNeeded reports whether the run was a clean no-op: every table was
// already in its final place.
func (r Result) NoWorkNeeded() bool {
	return len(r.Copied) == 0 && len(r.Dropped) == 0
}

// action describes what a table needs.
type action string

const (
	// actionCopy: present in source, absent from destination.
	actionCopy action = "copy"
	// actionDropOnly: present in both with matching counts — a prior run
	// committed the copy but died before or during the drop. Verify, then drop.
	actionDropOnly action = "drop-only"
	// actionDone: absent from source, present in destination. Fully migrated.
	actionDone action = "done"
)

// TableState is the observed per-table state used for both the dry-run report
// and the resume decision.
type TableState struct {
	Name       string
	InSource   bool
	InDest     bool
	SourceRows int64
	DestRows   int64
	Action     action
	// LedgerRows is the source row count recorded by the forward copy, when a
	// ledger entry exists. Used to corroborate already-migrated tables whose
	// source is gone.
	LedgerRows int64
	HasLedger  bool
}

// migrator holds the single connection with both files attached.
type migrator struct {
	opts   Options
	out    io.Writer
	db     *sql.DB
	conn   *sql.Conn
	source string // schema name: "main" (control) or "mem"
	dest   string
	// destPath/sourcePath are the on-disk files behind dest/source.
	destPath   string
	sourcePath string
	// memoryPreexisting records whether memory.db was on disk before this run
	// opened it. ATTACH materializes an empty file as a side effect, which a
	// --dry-run must not leave behind.
	memoryPreexisting bool
}

// Run executes a migration. It is idempotent and resumable: aborting at any
// point and re-running completes the job without duplicating rows, and running
// against an already-migrated pair of files is a clean no-op.
func Run(ctx context.Context, opts Options) (Result, error) {
	if strings.TrimSpace(opts.ControlPath) == "" {
		return Result{}, errors.New("control database path is required")
	}
	if strings.TrimSpace(opts.MemoryPath) == "" {
		opts.MemoryPath = filepath.Join(filepath.Dir(opts.ControlPath), "memory.db")
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	m := &migrator{opts: opts, out: opts.Out}
	if err := m.open(ctx); err != nil {
		return Result{}, err
	}
	defer m.close()
	return m.run(ctx)
}

func (m *migrator) open(ctx context.Context) error {
	if _, err := os.Stat(m.opts.ControlPath); err != nil {
		return fmt.Errorf("control database %s: %w", m.opts.ControlPath, err)
	}
	if _, err := os.Stat(m.opts.MemoryPath); err == nil {
		m.memoryPreexisting = true
	}
	// orchestrator.db is always "main"; memory.db is always attached as "mem".
	// Which of the two is source vs destination depends on direction.
	db, err := sql.Open("sqlite", m.opts.ControlPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", m.opts.ControlPath, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	m.db = db
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return fmt.Errorf("acquire connection: %w", err)
	}
	m.conn = conn

	// PRAGMA foreign_keys is a no-op inside a transaction and ATTACH is
	// disallowed inside one, so both happen here, before any transaction.
	//
	// FKs stay OFF for the whole run on purpose:
	//   - forward: the destination tables carry no FKs at all (cross-file FKs
	//     are impossible), so enforcement has nothing to do but would make the
	//     source DROP order significant.
	//   - reverse: the restored tables DO carry the sessions(id) FKs again, and
	//     we want the rows restored byte-faithfully. Enforcing mid-restore would
	//     reject a row whose session was deleted while the data lived in
	//     memory.db, i.e. it would block the rollback. We instead run
	//     foreign_key_check afterwards and report violations.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF; PRAGMA busy_timeout=10000;`); err != nil {
		m.close()
		return fmt.Errorf("set pragmas: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`ATTACH DATABASE %s AS mem`, sqlLiteral(m.opts.MemoryPath))); err != nil {
		m.close()
		return fmt.Errorf("attach %s: %w", m.opts.MemoryPath, err)
	}
	// journal_mode is per-database and must be set after ATTACH for mem. WAL
	// matches what the application opens memory.db with, so we do not leave the
	// file in a mode the orchestrator would then have to switch.
	//
	// Skipped for a dry run: setting journal_mode writes the database header,
	// which would leave a real (non-empty) memory.db behind for a command that
	// promised to change nothing.
	if !m.opts.DryRun {
		if _, err := conn.ExecContext(ctx, `PRAGMA mem.journal_mode=WAL`); err != nil {
			m.close()
			return fmt.Errorf("set mem journal_mode: %w", err)
		}
	}

	if m.opts.Direction == Reverse {
		m.source, m.dest = "mem", "main"
		m.sourcePath, m.destPath = m.opts.MemoryPath, m.opts.ControlPath
	} else {
		m.source, m.dest = "main", "mem"
		m.sourcePath, m.destPath = m.opts.ControlPath, m.opts.MemoryPath
	}
	return nil
}

func (m *migrator) close() {
	if m.conn != nil {
		_, _ = m.conn.ExecContext(context.Background(), `DETACH DATABASE mem`)
		_ = m.conn.Close()
		m.conn = nil
	}
	if m.db != nil {
		_ = m.db.Close()
		m.db = nil
	}
	// A dry run must be inert. ATTACH creates memory.db as a side effect even
	// when nothing is written, so remove the empty file we materialized (never a
	// file that already existed, and never one with content).
	if m.opts.DryRun && !m.memoryPreexisting {
		if info, err := os.Stat(m.opts.MemoryPath); err == nil && info.Size() == 0 {
			_ = os.Remove(m.opts.MemoryPath)
			_ = os.Remove(m.opts.MemoryPath + "-wal")
			_ = os.Remove(m.opts.MemoryPath + "-shm")
		}
	}
}

func (m *migrator) logf(format string, args ...any) {
	fmt.Fprintf(m.out, format+"\n", args...)
}

func (m *migrator) run(ctx context.Context) (Result, error) {
	result := Result{Direction: m.opts.Direction, DryRun: m.opts.DryRun}

	m.logf("dbsplit %s migration", m.opts.Direction)
	m.logf("  control (orchestrator.db): %s", m.opts.ControlPath)
	m.logf("  memory  (memory.db):       %s", m.opts.MemoryPath)
	m.logf("  source -> destination:     %s -> %s", m.schemaLabel(m.source), m.schemaLabel(m.dest))
	if m.opts.DryRun {
		m.logf("  MODE: dry run (nothing will be changed)")
	}
	m.logf("")

	states, err := m.inspect(ctx)
	if err != nil {
		return result, err
	}
	result.Tables = states
	m.reportStates(states)

	if err := m.reportDisk(ctx); err != nil {
		return result, err
	}

	var toCopy, toDrop []string
	for _, st := range states {
		switch st.Action {
		case actionCopy:
			toCopy = append(toCopy, st.Name)
			toDrop = append(toDrop, st.Name)
		case actionDropOnly:
			toDrop = append(toDrop, st.Name)
		case actionDone:
			result.AlreadyDone = append(result.AlreadyDone, st.Name)
		}
	}

	m.logf("")
	if len(toCopy) == 0 && len(toDrop) == 0 {
		m.logf("PLAN: nothing to do — all %d tables are already in %s. This run is a no-op.",
			len(MemoryTables), m.schemaLabel(m.dest))
		if m.opts.DryRun {
			return result, nil
		}
		m.logf("")
		m.logf("OK: %s migration already complete; no changes made.", m.opts.Direction)
		return result, nil
	}
	m.logf("PLAN:")
	if len(toCopy) > 0 {
		m.logf("  1. copy %d table(s) to %s: %s", len(toCopy), m.schemaLabel(m.dest), strings.Join(toCopy, ", "))
	} else {
		m.logf("  1. copy: nothing (all copies already committed by a previous run)")
	}
	m.logf("  2. verify row counts + column signatures for %d table(s)", len(toDrop))
	m.logf("  3. drop %d table(s) from %s", len(toDrop), m.schemaLabel(m.source))
	if m.opts.SkipVacuum {
		m.logf("  4. VACUUM: skipped (--no-vacuum)")
	} else {
		m.logf("  4. VACUUM %s", m.schemaLabel(m.source))
	}
	if len(result.AlreadyDone) > 0 {
		m.logf("  (%d table(s) already migrated by a previous run: %s)",
			len(result.AlreadyDone), strings.Join(result.AlreadyDone, ", "))
	}

	if m.opts.DryRun {
		m.logf("")
		m.logf("dry run complete; no changes made.")
		return result, nil
	}

	m.logf("")
	m.logf("== step 1/4: copy ==")
	for _, name := range toCopy {
		if err := m.copyTable(ctx, name); err != nil {
			return result, fmt.Errorf("copy %s: %w", name, err)
		}
		result.Copied = append(result.Copied, name)
		if m.opts.OnTableCopied != nil {
			if err := m.opts.OnTableCopied(name); err != nil {
				m.logf("")
				m.logf("ABORTED after copying %d table(s): %v", len(result.Copied), err)
				m.logf("Nothing has been dropped. Re-run the same command to resume.")
				return result, err
			}
		}
	}
	if len(toCopy) == 0 {
		m.logf("  (nothing to copy)")
	}

	m.logf("")
	m.logf("== step 2/4: verify (hard gate before any drop) ==")
	if err := m.verify(ctx, toDrop); err != nil {
		m.logf("")
		m.logf("VERIFICATION FAILED — nothing was dropped, no data was destroyed.")
		m.logf("The source database %s is untouched and still authoritative.", m.sourcePath)
		m.logf("Investigate before re-running. Re-running is safe: the copy phase is idempotent.")
		return result, err
	}

	m.logf("")
	m.logf("== step 3/4: drop from %s ==", m.schemaLabel(m.source))
	for _, name := range toDrop {
		if err := m.dropTable(ctx, name); err != nil {
			return result, fmt.Errorf("drop %s: %w", name, err)
		}
		result.Dropped = append(result.Dropped, name)
	}

	if m.opts.Direction == Reverse {
		violations, err := m.foreignKeyCheck(ctx)
		if err != nil {
			return result, err
		}
		result.FKViolations = violations
	}

	m.logf("")
	m.logf("== step 4/4: reclaim space ==")
	if m.opts.SkipVacuum {
		m.logf("  VACUUM skipped (--no-vacuum). Freed pages remain available for reuse")
		m.logf("  inside %s but are not returned to the filesystem.", m.sourcePath)
	} else {
		freed, err := m.vacuum(ctx)
		if err != nil {
			// The data move is committed and verified at this point; a failed
			// VACUUM is a space-reclamation problem, not a data problem.
			m.logf("  WARNING: VACUUM failed: %v", err)
			m.logf("  The migration itself is COMPLETE and verified. Freed pages stay")
			m.logf("  available for reuse inside the file. You may retry VACUUM later.")
		} else {
			result.Vacuumed = true
			result.FreedBytes = freed
			m.logf("  VACUUM done; %s returned to the filesystem.", humanBytes(freed))
		}
	}

	m.logf("")
	m.logf("OK: %s migration complete. %d copied, %d dropped from %s.",
		m.opts.Direction, len(result.Copied), len(result.Dropped), m.schemaLabel(m.source))
	if result.FKViolations > 0 {
		m.logf("NOTE: %d foreign-key violation(s) remain in %s (rows whose session was",
			result.FKViolations, m.schemaLabel(m.dest))
		m.logf("      deleted while the data lived in memory.db). The rollback restored the")
		m.logf("      rows faithfully rather than dropping them. Run PRAGMA foreign_key_check")
		m.logf("      to list them.")
	}
	return result, nil
}

func (m *migrator) schemaLabel(schema string) string {
	if schema == "mem" {
		return "memory.db"
	}
	return "orchestrator.db"
}

// inspect determines each table's state from the two files themselves. This is
// the resumability mechanism: because every copy commits in a single
// destination-only transaction, a table can only ever be absent-from-dest (copy
// not committed) or present-and-complete (copy committed). There is no partial
// state to reason about.
func (m *migrator) inspect(ctx context.Context) ([]TableState, error) {
	ledger, err := m.readLedger(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]TableState, 0, len(MemoryTables))
	for _, name := range MemoryTables {
		st := TableState{Name: name}
		if st.InSource, err = m.tableExists(ctx, m.source, name); err != nil {
			return nil, err
		}
		if st.InDest, err = m.tableExists(ctx, m.dest, name); err != nil {
			return nil, err
		}
		if entry, ok := ledger[name]; ok {
			st.HasLedger = true
			st.LedgerRows = entry.SourceRows
		}
		if st.InSource {
			if st.SourceRows, err = m.countRows(ctx, m.source, name); err != nil {
				return nil, err
			}
		}
		if st.InDest {
			if st.DestRows, err = m.countRows(ctx, m.dest, name); err != nil {
				return nil, err
			}
		}
		switch {
		case st.InSource && !st.InDest:
			st.Action = actionCopy
		case st.InSource && st.InDest:
			// A previous run committed the copy but died before the drop
			// completed. Fall through to the verify gate, which compares counts
			// and column signatures and refuses to drop on any mismatch.
			st.Action = actionDropOnly
		case !st.InSource && st.InDest:
			st.Action = actionDone
		default:
			return nil, fmt.Errorf("table %s is missing from BOTH %s and %s; "+
				"refusing to continue. Restore from backup before proceeding",
				name, m.schemaLabel(m.source), m.schemaLabel(m.dest))
		}
		states = append(states, st)
	}
	return states, nil
}

func (m *migrator) reportStates(states []TableState) {
	m.logf("current state (%-20s %10s %10s  %s):", "table", m.schemaLabel(m.source), m.schemaLabel(m.dest), "action")
	for _, st := range states {
		src, dst := "-", "-"
		if st.InSource {
			src = fmt.Sprintf("%d", st.SourceRows)
		}
		if st.InDest {
			dst = fmt.Sprintf("%d", st.DestRows)
		}
		note := ""
		switch st.Action {
		case actionDone:
			note = "already migrated"
			if st.HasLedger {
				note = fmt.Sprintf("already migrated (ledger recorded %d source rows)", st.LedgerRows)
			}
		case actionDropOnly:
			note = "copy already committed; needs verify + drop"
		case actionCopy:
			note = "to copy"
		}
		m.logf("  %-22s %10s %10s  %s", st.Name, src, dst, note)
	}
}

// copyTable recreates one table in the destination and copies its rows, in a
// single destination-only transaction.
//
// Destination-only matters twice over. It makes the unit atomic (SQLite cannot
// atomically commit a transaction spanning multiple databases when any of them
// is in WAL mode, and both of these are), which is what makes the run resumable
// without a separate progress ledger. And it guarantees the source is never
// written during the copy phase, so an abort here cannot lose data.
func (m *migrator) copyTable(ctx context.Context, name string) error {
	srcDDL, err := m.tableDDL(ctx, m.source, name)
	if err != nil {
		return err
	}
	srcIndexes, err := m.indexDDL(ctx, m.source, name)
	if err != nil {
		return err
	}
	columns, err := m.columnNames(ctx, m.source, name)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return fmt.Errorf("no columns found")
	}

	var createStmt string
	var droppedFKs []string
	indexStmts := srcIndexes
	exactRestore := false

	switch {
	case m.opts.Direction == Reverse:
		// Rollback restores orchestrator.db's original schema. The forward run
		// recorded each table's verbatim pre-migration DDL, and the reverse
		// destination is always "main" — where an unqualified CREATE TABLE
		// lands — so the recorded statement can be replayed byte-for-byte with
		// no rewriting at all. That is what makes the restored sqlite_master
		// text identical to the pre-migration original, FK clauses included.
		recorded, ok, lerr := m.ledgerDDL(ctx, name)
		if lerr != nil {
			return lerr
		}
		if ok {
			createStmt = recorded
			exactRestore = true
		} else if tablesWithSessionFK[name] {
			m.logf("  %-22s note: no ledger entry; reconstructing the sessions(id) FK", name)
			createStmt, err = restoreSessionFK(srcDDL, m.dest)
			if err != nil {
				return err
			}
		} else {
			createStmt, err = requalifyCreateTable(srcDDL, m.dest)
			if err != nil {
				return err
			}
		}
		// Prefer the index DDL recorded at forward-migration time, for the same
		// exact-restoration reason.
		if recordedIdx, ok, lerr := m.ledgerIndexes(ctx, name); lerr != nil {
			return lerr
		} else if ok {
			indexStmts = recordedIdx
		}

	case tablesWithSessionFK[name]:
		// Forward: cross-database foreign keys are impossible in SQLite, so the
		// sessions(id) references must go. Everything else — PKs, CHECKs,
		// UNIQUE, NOT NULL, DEFAULTs — is preserved verbatim.
		createStmt, droppedFKs, err = stripSessionFKs(srcDDL, m.dest)
		if err != nil {
			return err
		}

	default:
		createStmt, err = requalifyCreateTable(srcDDL, m.dest)
		if err != nil {
			return err
		}
	}

	rows, err := m.countRows(ctx, m.source, name)
	if err != nil {
		return err
	}

	tx, err := m.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("create destination table: %w (stmt: %s)", err, createStmt)
	}
	for _, idx := range indexStmts {
		stmt := idx
		if !exactRestore {
			// Forward (or a ledger-less rollback): the stored DDL is
			// unqualified, so point it at the destination schema explicitly.
			stmt, err = requalifyCreateIndex(idx, m.dest)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create destination index: %w (stmt: %s)", err, stmt)
		}
	}

	// Explicit column lists rather than INSERT ... SELECT *: the destination was
	// created from the source DDL so the orders agree, but naming the columns
	// makes that an assertion instead of an assumption, and it survives a future
	// column added in a different position.
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = quoteIdent(c)
	}
	colList := strings.Join(quoted, ", ")
	insert := fmt.Sprintf(`INSERT INTO %s.%s (%s) SELECT %s FROM %s.%s`,
		quoteIdent(m.dest), quoteIdent(name), colList, colList, quoteIdent(m.source), quoteIdent(name))
	res, err := tx.ExecContext(ctx, insert)
	if err != nil {
		return fmt.Errorf("copy rows: %w", err)
	}
	inserted, _ := res.RowsAffected()

	// The ledger lives in memory.db in both directions, so it is only written
	// when memory.db is the destination (forward). Keeping the ledger out of
	// orchestrator.db is what lets the reverse migration restore that file's
	// schema exactly, with no migration bookkeeping left behind.
	if m.opts.Direction == Forward {
		if err := m.writeLedgerTx(ctx, tx, name, rows, srcDDL, srcIndexes); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit copy: %w", err)
	}

	detail := fmt.Sprintf("%d rows, %d columns, %d index(es)", inserted, len(columns), len(indexStmts))
	if len(droppedFKs) > 0 {
		detail += fmt.Sprintf(", dropped %d sessions(id) FK", len(droppedFKs))
	}
	if m.opts.Direction == Reverse && tablesWithSessionFK[name] {
		detail += ", restored sessions(id) FK"
	}
	m.logf("  %-22s copied: %s", name, detail)
	return nil
}

// verify is the hard gate. It compares row counts AND column signatures between
// source and destination for every table about to be dropped. A dropped column
// would sail through a count check, so the signature comparison is not
// optional.
func (m *migrator) verify(ctx context.Context, tables []string) error {
	var problems []string
	for _, name := range tables {
		srcRows, err := m.countRows(ctx, m.source, name)
		if err != nil {
			return err
		}
		destRows, err := m.countRows(ctx, m.dest, name)
		if err != nil {
			return err
		}
		srcCols, err := m.columnSignature(ctx, m.source, name)
		if err != nil {
			return err
		}
		destCols, err := m.columnSignature(ctx, m.dest, name)
		if err != nil {
			return err
		}

		var issues []string
		if srcRows != destRows {
			issues = append(issues, fmt.Sprintf("row count %d != %d", srcRows, destRows))
		}
		if diff := diffSignatures(srcCols, destCols); diff != "" {
			issues = append(issues, "schema: "+diff)
		}
		if len(issues) == 0 {
			m.logf("  %-22s OK  rows=%-8d columns=%d", name, srcRows, len(srcCols))
			continue
		}
		m.logf("  %-22s MISMATCH  %s", name, strings.Join(issues, "; "))
		problems = append(problems, fmt.Sprintf("%s: %s", name, strings.Join(issues, "; ")))
	}
	if len(problems) > 0 {
		return fmt.Errorf("verification failed for %d table(s): %s", len(problems), strings.Join(problems, " | "))
	}
	m.logf("  all %d table(s) verified: row counts and column signatures match", len(tables))
	return nil
}

// dropTable removes one table from the source in a source-only transaction.
// On rollback it also clears the table's ledger entry so a subsequent forward
// run starts from a clean slate.
func (m *migrator) dropTable(ctx context.Context, name string) error {
	rows, err := m.countRows(ctx, m.source, name)
	if err != nil {
		return err
	}
	tx, err := m.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE %s.%s`, quoteIdent(m.source), quoteIdent(name))); err != nil {
		return err
	}
	if m.opts.Direction == Reverse {
		// Source is memory.db here, so the ledger delete is in the same
		// database as the drop and the unit stays atomic.
		ok, err := m.tableExistsTx(ctx, tx, "mem", ledgerTable)
		if err != nil {
			return err
		}
		if ok {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM mem.%s WHERE table_name = ?`, quoteIdent(ledgerTable)), name); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.logf("  %-22s dropped from %s (%d rows released)", name, m.schemaLabel(m.source), rows)
	return nil
}

// foreignKeyCheck reports rows in the control database that violate a foreign
// key after a rollback. It never fails the run: refusing to complete a rollback
// because a session row was deleted in the meantime would be worse than a
// dangling row the schema's CASCADE would have removed anyway.
func (m *migrator) foreignKeyCheck(ctx context.Context) (int, error) {
	rows, err := m.conn.QueryContext(ctx, `PRAGMA main.foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	count := 0
	byTable := map[string]int{}
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return 0, fmt.Errorf("foreign_key_check scan: %w", err)
		}
		count++
		byTable[table]++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		m.logf("  foreign_key_check: clean (%s FK enforcement restored)", m.schemaLabel(m.dest))
		return 0, nil
	}
	names := make([]string, 0, len(byTable))
	for t := range byTable {
		names = append(names, fmt.Sprintf("%s=%d", t, byTable[t]))
	}
	sort.Strings(names)
	m.logf("  foreign_key_check: %d violation(s) [%s]", count, strings.Join(names, " "))
	return count, nil
}

// vacuum rebuilds the source file to return freed pages to the filesystem. It
// checks free space first: VACUUM writes a full second copy of the database
// before swapping, so dying mid-VACUUM for want of disk is an avoidable
// self-inflicted outage.
func (m *migrator) vacuum(ctx context.Context) (int64, error) {
	before, err := dbFileSize(m.sourcePath)
	if err != nil {
		return 0, err
	}
	free, err := freeBytes(filepath.Dir(m.sourcePath))
	if err != nil {
		m.logf("  free space: unknown (%v); proceeding", err)
	} else {
		m.logf("  free space: %s available, VACUUM needs roughly %s", humanBytes(free), humanBytes(before))
		if free < before {
			return 0, fmt.Errorf("insufficient free space for VACUUM: %s available, roughly %s needed. "+
				"Free space and re-run, or skip with --no-vacuum (the migration itself is already complete)",
				humanBytes(free), humanBytes(before))
		}
	}
	m.logf("  VACUUM %s ...", m.schemaLabel(m.source))
	if _, err := m.conn.ExecContext(ctx, fmt.Sprintf(`VACUUM %s`, quoteIdent(m.source))); err != nil {
		return 0, err
	}
	after, err := dbFileSize(m.sourcePath)
	if err != nil {
		return 0, err
	}
	if after > before {
		return 0, nil
	}
	return before - after, nil
}

// reportDisk gives the operator the pre-flight numbers before anything happens.
func (m *migrator) reportDisk(ctx context.Context) error {
	m.logf("")
	srcSize, err := dbFileSize(m.sourcePath)
	if err != nil {
		return err
	}
	m.logf("disk: %s is %s", m.schemaLabel(m.source), humanBytes(srcSize))
	if destSize, err := dbFileSize(m.destPath); err == nil {
		m.logf("      %s is %s", m.schemaLabel(m.dest), humanBytes(destSize))
	} else if os.IsNotExist(err) {
		m.logf("      %s does not exist yet (it will be created)", m.schemaLabel(m.dest))
	}
	if free, err := freeBytes(filepath.Dir(m.sourcePath)); err == nil {
		m.logf("      %s free on %s", humanBytes(free), filepath.Dir(m.sourcePath))
		if !m.opts.SkipVacuum && free < srcSize {
			m.logf("      WARNING: free space is below the source size; the VACUUM step needs")
			m.logf("      roughly that much and will be refused. Free space or use --no-vacuum.")
		}
	} else {
		m.logf("      free space: unknown (%v)", err)
	}
	return nil
}

// ---- introspection helpers (all read from the live files, never from Go literals) ----

func (m *migrator) tableExists(ctx context.Context, schema, name string) (bool, error) {
	q := fmt.Sprintf(`SELECT count(*) FROM %s.sqlite_master WHERE type='table' AND name=?`, quoteIdent(schema))
	var n int
	if err := m.conn.QueryRowContext(ctx, q, name).Scan(&n); err != nil {
		return false, fmt.Errorf("check %s.%s exists: %w", schema, name, err)
	}
	return n > 0, nil
}

func (m *migrator) tableExistsTx(ctx context.Context, tx *sql.Tx, schema, name string) (bool, error) {
	q := fmt.Sprintf(`SELECT count(*) FROM %s.sqlite_master WHERE type='table' AND name=?`, quoteIdent(schema))
	var n int
	if err := tx.QueryRowContext(ctx, q, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (m *migrator) countRows(ctx context.Context, schema, name string) (int64, error) {
	var n int64
	q := fmt.Sprintf(`SELECT count(*) FROM %s.%s`, quoteIdent(schema), quoteIdent(name))
	if err := m.conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s.%s: %w", schema, name, err)
	}
	return n, nil
}

// tableDDL returns the table's CREATE statement as SQLite currently stores it,
// which includes every ALTER TABLE ADD COLUMN applied since the table was made.
func (m *migrator) tableDDL(ctx context.Context, schema, name string) (string, error) {
	q := fmt.Sprintf(`SELECT sql FROM %s.sqlite_master WHERE type='table' AND name=?`, quoteIdent(schema))
	var ddl sql.NullString
	if err := m.conn.QueryRowContext(ctx, q, name).Scan(&ddl); err != nil {
		return "", fmt.Errorf("read DDL for %s.%s: %w", schema, name, err)
	}
	if !ddl.Valid || strings.TrimSpace(ddl.String) == "" {
		return "", fmt.Errorf("%s.%s has no stored DDL", schema, name)
	}
	return ddl.String, nil
}

// indexDDL returns the explicit CREATE INDEX statements for a table. Implicit
// indexes (sqlite_autoindex_*) have no stored SQL and are recreated
// automatically by the PRIMARY KEY / UNIQUE clauses we copy verbatim.
func (m *migrator) indexDDL(ctx context.Context, schema, table string) ([]string, error) {
	q := fmt.Sprintf(
		`SELECT sql FROM %s.sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL ORDER BY name`,
		quoteIdent(schema))
	rows, err := m.conn.QueryContext(ctx, q, table)
	if err != nil {
		return nil, fmt.Errorf("read indexes for %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			return nil, err
		}
		out = append(out, ddl)
	}
	return out, rows.Err()
}

func (m *migrator) columnNames(ctx context.Context, schema, table string) ([]string, error) {
	cols, err := m.tableInfo(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.name)
	}
	return out, nil
}

type columnInfo struct {
	name    string
	typ     string
	notNull int
	dflt    sql.NullString
	pk      int
}

func (m *migrator) tableInfo(ctx context.Context, schema, table string) ([]columnInfo, error) {
	// pragma_table_info is a table-valued function; the schema is selected via
	// its second argument so a single query works for main and mem alike.
	rows, err := m.conn.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?, ?) ORDER BY cid`, table, schema)
	if err != nil {
		return nil, fmt.Errorf("table_info %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	var out []columnInfo
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.name, &c.typ, &c.notNull, &c.dflt, &c.pk); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// columnSignature renders a table's live column layout as comparable strings:
// name, declared type, NOT NULL, DEFAULT and PK position. This is what catches a
// column silently lost in the copy, which a row-count check cannot see.
func (m *migrator) columnSignature(ctx context.Context, schema, table string) ([]string, error) {
	cols, err := m.tableInfo(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		dflt := "<none>"
		if c.dflt.Valid {
			dflt = c.dflt.String
		}
		out = append(out, fmt.Sprintf("%s %s notnull=%d default=%s pk=%d", c.name, c.typ, c.notNull, dflt, c.pk))
	}
	return out, nil
}

func diffSignatures(want, got []string) string {
	if len(want) != len(got) {
		return fmt.Sprintf("column count %d != %d (%s vs %s)", len(want), len(got),
			strings.Join(shortNames(want), ","), strings.Join(shortNames(got), ","))
	}
	for i := range want {
		if want[i] != got[i] {
			return fmt.Sprintf("column %d differs: %q != %q", i, want[i], got[i])
		}
	}
	return ""
}

func shortNames(sig []string) []string {
	out := make([]string, 0, len(sig))
	for _, s := range sig {
		out = append(out, strings.Fields(s)[0])
	}
	return out
}

// ---- ledger ----

type ledgerEntry struct {
	SourceRows int64
	SourceDDL  string
	Indexes    []string
}

func (m *migrator) readLedger(ctx context.Context) (map[string]ledgerEntry, error) {
	out := map[string]ledgerEntry{}
	ok, err := m.tableExists(ctx, "mem", ledgerTable)
	if err != nil || !ok {
		return out, err
	}
	rows, err := m.conn.QueryContext(ctx, fmt.Sprintf(
		`SELECT table_name, source_rows, source_ddl, source_indexes_json FROM mem.%s`, quoteIdent(ledgerTable)))
	if err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, ddl, idxJSON string
		var count int64
		if err := rows.Scan(&name, &count, &ddl, &idxJSON); err != nil {
			return nil, err
		}
		var indexes []string
		// A malformed ledger must not block the run; the DDL fallback covers it.
		_ = json.Unmarshal([]byte(idxJSON), &indexes)
		out[name] = ledgerEntry{SourceRows: count, SourceDDL: ddl, Indexes: indexes}
	}
	return out, rows.Err()
}

func (m *migrator) ledgerDDL(ctx context.Context, name string) (string, bool, error) {
	ledger, err := m.readLedger(ctx)
	if err != nil {
		return "", false, err
	}
	entry, ok := ledger[name]
	if !ok || strings.TrimSpace(entry.SourceDDL) == "" {
		return "", false, nil
	}
	return entry.SourceDDL, true, nil
}

func (m *migrator) ledgerIndexes(ctx context.Context, name string) ([]string, bool, error) {
	ledger, err := m.readLedger(ctx)
	if err != nil {
		return nil, false, err
	}
	entry, ok := ledger[name]
	if !ok || len(entry.Indexes) == 0 {
		return nil, false, nil
	}
	return entry.Indexes, true, nil
}

func (m *migrator) writeLedgerTx(ctx context.Context, tx *sql.Tx, name string, rows int64, ddl string, indexes []string) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS mem.%s (
  table_name          TEXT PRIMARY KEY,
  direction           TEXT NOT NULL,
  source_rows         INTEGER NOT NULL,
  source_ddl          TEXT NOT NULL,
  source_indexes_json TEXT NOT NULL DEFAULT '[]',
  copied_at           TEXT NOT NULL
)`, quoteIdent(ledgerTable))); err != nil {
		return fmt.Errorf("create ledger: %w", err)
	}
	if indexes == nil {
		indexes = []string{}
	}
	idxJSON, err := json.Marshal(indexes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO mem.%s (table_name, direction, source_rows, source_ddl, source_indexes_json, copied_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(table_name) DO UPDATE SET
  direction = excluded.direction,
  source_rows = excluded.source_rows,
  source_ddl = excluded.source_ddl,
  source_indexes_json = excluded.source_indexes_json,
  copied_at = excluded.copied_at`, quoteIdent(ledgerTable)),
		name, Forward.String(), rows, ddl, string(idxJSON), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("write ledger: %w", err)
	}
	return nil
}

// ---- misc ----

// dbFileSize sums a SQLite database's main file and its WAL sidecar, since WAL
// content is part of the logical database size for planning purposes.
func dbFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	total := info.Size()
	if wal, err := os.Stat(path + "-wal"); err == nil {
		total += wal.Size()
	}
	return total, nil
}

// sqlLiteral quotes a string for inline use where SQLite forbids a bound
// parameter (ATTACH DATABASE's filename).
func sqlLiteral(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", value)
}
