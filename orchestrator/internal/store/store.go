package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/sqlitex"
)

type Session struct {
	ID                    string     `json:"id"`
	UserID                string     `json:"user_id"`
	Status                string     `json:"status"`
	Agent                 string     `json:"agent"`
	Mode                  string     `json:"mode"`
	Workspace             string     `json:"workspace"`
	AgentHomePath         string     `json:"agent_home_path,omitempty"`
	ActiveGenerationID    string     `json:"active_generation_id,omitempty"`
	RestoreID             string     `json:"restore_id"`
	RestoreMS             *int64     `json:"restore_ms,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	EndedAt               *time.Time `json:"ended_at,omitempty"`
	LastActivityAt        *time.Time `json:"last_activity_at,omitempty"`
	CheckpointPath        string     `json:"checkpoint_path,omitempty"`
	AutoCheckpointEnabled bool       `json:"auto_checkpoint_enabled"`
	FailureReason         string     `json:"failure_reason,omitempty"`
	ErrorClass            string     `json:"error_class,omitempty"`
	Title                 string     `json:"title"`
	TitleCustom           bool       `json:"title_custom"`
}

type DestroySessionResult struct {
	GenerationIDs []string
	EventID       int64
}

type InterruptSessionParams struct {
	SessionID    string
	GenerationID string
	OwnerUUID    string
	ErrorClass   string
	Reason       string
	Now          time.Time
}

type InterruptSessionResult struct {
	SessionStatus string
	EventIDs      []int64
	// PendingQueued counts the turns Stop parked. They survive the interrupt and
	// stay visible, but nothing will dispatch them until the user releases each
	// one. Reported so the UI can say so rather than leaving the user to guess
	// whether their queue is about to run.
	PendingQueued int64
	// InterruptedTurnID is the turn that was executing, or 0 when none was. The
	// caller needs it to address the abort at the sandbox: a control command names
	// its target turn so a late delivery cannot stop the turn that came after.
	InterruptedTurnID int64
	// Runtime is the post-interrupt projection, taken inside the same
	// transaction.
	Runtime SessionRuntimeSnapshot
}

type Message struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	TurnID    *int64    `json:"turn_id,omitempty"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	// StopReason is pi's terminator for an assistant message: "stop" for a
	// turn's final reply, "toolUse" for an intermediate tool-calling step, or ""
	// when unknown (user messages, shell output, salvaged text, pre-migration
	// history). Lets the client mark a completed answer vs. an interstitial.
	StopReason string `json:"stop_reason,omitempty"`
	// Trace holds the execution-trace nodes (thinking phases + tool calls) that
	// produced this assistant message. Nil when the message has no stored trace
	// (user messages, shell output, pre-trace history). Serialized to the client
	// verbatim; the orchestrator does not interpret node contents.
	Trace json.RawMessage `json:"trace,omitempty"`
	// Feedback is the requesting user's thumbs rating for this message
	// ("up"/"down"), or empty when unset. Populated by ListMessages.
	Feedback string `json:"feedback,omitempty"`
	// Bounded transcript projections set these fields. They remain omitted by
	// the legacy full-history endpoint for wire compatibility.
	ContentTruncated bool  `json:"content_truncated,omitempty"`
	ContentLength    int   `json:"content_length,omitempty"`
	HasTrace         bool  `json:"has_trace,omitempty"`
	IsFinalReply     *bool `json:"is_final_reply,omitempty"`
}

type EnqueueTurnMessageParams struct {
	SessionID string
	Content   string
	Now       time.Time
}

type EnqueueTurnMessageResult struct {
	TurnID int64
	// Runtime is the session's projection as of this enqueue: the authoritative
	// queue state the caller hands straight back to the client.
	//
	// No Message field. Enqueuing does not create one — the turn becomes a
	// transcript entry when the agent starts it (materializeTurnMessageTx).
	Runtime SessionRuntimeSnapshot
}

type Artifact struct {
	SessionID string    `json:"session_id"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the CONTROL-PLANE store: sessions, messages, turns, runtime
// generations, resources, notifications, auth. It owns control.db.
//
// The memory/distillation half (trajectory events, episode/semantic memories,
// skill candidates, evolution runs) lives in a physically separate database
// behind internal/memorystore. This package has NO knowledge of that package;
// the dependency runs one way only, control -> memory, via the ingest-boundary
// interfaces memorystore declares for itself (see memorystore/ingest.go).
type Store struct {
	// db is the writer pool: exactly one connection, opened with
	// _txlock=immediate. Every method on Store currently runs through it, so
	// concurrency behavior is identical to the single-pool setup this replaced.
	db *sql.DB
	// readDB is the reader pool. It is intentionally NOT used by any query
	// method yet: this change only establishes the infrastructure so that
	// migrating read paths off the writer is a separate, reviewable diff. To
	// move a read query over, swap its s.db for s.readDB (and keep anything
	// that writes, or that reads inside a write transaction, on s.db).
	readDB  *sql.DB
	options Options
}

type Options struct {
	// Managed requires a verified explicit migration and never runs schema DDL.
	Managed bool

	AgentHomesRoot string
	// ReaderPoolSize caps the reader pool. Zero means defaultReaderPoolSize.
	// Each modernc.org/sqlite connection carries its own libc.TLS, so
	// connections are not free; keep this small.
	ReaderPoolSize int
}

type SessionActiveGenerationCASParams struct {
	SessionID            string
	ExpectedGenerationID sql.NullString
	NextGenerationID     string
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, Options{})
}

// OpenForMaintenance opens an existing database for a short-lived out-of-band
// task (e.g. the `orchestrator user` subcommands) while the server may be
// running against the same file. It applies the connection PRAGMAs but
// deliberately SKIPS migrate(): schema setup includes ALTER TABLE statements
// and an FTS rebuild, which a side process must not run under a live server.
// A missing database file is an error here rather than something to create.
//
// The PRAGMAs come from the shared DSN builder rather than a one-shot
// ExecContext. They are per-connection in SQLite, so configuring them after
// Open only reaches whichever connection happened to serve that statement —
// which works by accident at one connection and silently drops FK enforcement
// the moment the pool can hold more. VerifyPragmas reads them back so a
// misspelled pragma (which the driver accepts silently) fails here instead of
// disabling foreign keys in a maintenance command.
//
// No reader pool: this is a short-lived single-threaded command, so readDB
// stays nil and every method runs on the writer as usual.
func OpenForMaintenance(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db, err := sqlitex.OpenWriter(path)
	if err != nil {
		return nil, err
	}
	if err := sqlitex.VerifyPragmas(ctx, db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure database %s: %w", path, err)
	}
	return &Store{db: db, options: Options{AgentHomesRoot: "/var/lib/harness/agent-homes"}}, nil
}

func OpenWithOptions(ctx context.Context, path string, options Options) (*Store, error) {
	if strings.TrimSpace(options.AgentHomesRoot) == "" {
		options.AgentHomesRoot = "/var/lib/harness/agent-homes"
	}
	if options.ReaderPoolSize <= 0 {
		options.ReaderPoolSize = sqlitex.DefaultReaderPoolSize
	}

	if options.Managed {
		if err := schemastate.Verify(ctx, path, "store"); err != nil {
			return nil, err
		}
	}
	writer, err := sqlitex.OpenWriter(path)
	if err != nil {
		return nil, err
	}

	store := &Store{db: writer, options: options}
	// The writer creates the file and owns schema changes. Migrating before
	// the reader pool exists means no reader can observe a half-built schema.
	if err := func() error {
		if options.Managed {
			return nil
		}
		return store.migrate(ctx)
	}(); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := sqlitex.VerifyPragmas(ctx, writer, 1); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("writer pool: %w", err)
	}

	reader, err := sqlitex.OpenReader(ctx, path, options.ReaderPoolSize)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	store.readDB = reader
	return store, nil
}

func (s *Store) Close() error {
	var readerErr error
	if s.readDB != nil {
		readerErr = s.readDB.Close()
	}
	return errors.Join(s.db.Close(), readerErr)
}

// DBForTest returns the writer handle. Tests reach through it for direct SQL,
// and every Store method runs on the writer, so this stays the writer pool.
func (s *Store) DBForTest() *sql.DB {
	return s.db
}

// ReadDBForTest returns the reader pool handle.
func (s *Store) ReadDBForTest() *sql.DB {
	return s.readDB
}

func (s *Store) migrate(ctx context.Context) error {
	return s.ensureSchema(ctx)
}

func (s *Store) EnsureUser(ctx context.Context, id, name string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (id, display_name, created_at)
VALUES (?, ?, ?)
ON CONFLICT(id) DO NOTHING`, id, name, now)
	return err
}

// CreateSession persists a new session row.
//
// Domain admission rules -- "is this status a real session state" and "is this
// driver one the product supports" -- are the caller's job (see
// server.validateNewSession); they are policy about what a user may ask for, not
// invariants of the sessions table. Two persistence-level guards remain here and
// still reject the same bad input: the mode derivation below fails closed for any
// driver not in the agents catalog, and the sessions table's own
// CHECK(status IN ...) / CHECK(driver_id IN ...) constraints (schema.go) reject a
// bogus status or driver at write time.
func (s *Store) CreateSession(ctx context.Context, session Session) error {
	session.Mode = strings.TrimSpace(session.Mode)
	if session.Mode == "" {
		session.Mode = ModeForDriver(session.Agent)
	}
	if session.Mode == "" {
		return fmt.Errorf("unsupported session mode for driver %q", session.Agent)
	}
	if strings.TrimSpace(session.AgentHomePath) == "" {
		session.AgentHomePath = filepath.Join(s.options.AgentHomesRoot, session.ID)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (
  id, user_id, status, driver_id, mode, workspace, agent_home_path, restore_id,
  created_at, updated_at, expires_at, auto_checkpoint_enabled
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.UserID, session.Status, session.Agent, session.Mode, session.Workspace, nullableString(session.AgentHomePath),
		session.RestoreID,
		formatTime(session.CreatedAt), formatTime(session.UpdatedAt), formatOptionalTime(session.ExpiresAt),
		boolInt(session.AutoCheckpointEnabled),
	)
	return err
}

// sessionSelectColumns is the 21-column projection consumed by scanSession. It
// is shared by every "SELECT ... FROM sessions" query so the column order stays
// in lockstep with the scan helper; only the WHERE/ORDER clauses differ.
const sessionSelectColumns = `id, user_id, status, driver_id, mode, workspace, agent_home_path, active_generation_id, restore_id, restore_ms, created_at, updated_at, expires_at, ended_at, last_activity_at, checkpoint_path, auto_checkpoint_enabled, failure_reason, error_class, title, title_custom`

// prefixedSessionSelectColumns is sessionSelectColumns qualified with a table
// alias, for joins where a bare column name would be ambiguous.
func prefixedSessionSelectColumns(alias string) string {
	columns := strings.Split(sessionSelectColumns, ", ")
	for i, column := range columns {
		columns[i] = alias + "." + column
	}
	return strings.Join(columns, ", ")
}

// reader returns the reader pool, falling back to the writer when no reader
// exists (OpenForMaintenance leaves readDB nil). Read-only queries that were
// added after the pool split use this; pre-existing queries stay on the writer
// until their migration is reviewed on its own.
func (s *Store) reader() *sql.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

// Reads on the reader pool. These three are the highest-traffic read paths in the
// process — GetSession runs per streamed event for the SSE visibility check, and
// the message/session lists run on every poll and reload. Leaving them on the
// single writer connection made them queue against turn-lease heartbeats and
// output appends, which is the contention that lapsed leases and produced
// spurious model failures. Under WAL a reader never blocks the writer.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row := s.reader().QueryRowContext(ctx, `
SELECT `+sessionSelectColumns+`
FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT `+sessionSelectColumns+`
FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) CountActiveSessions(ctx context.Context) (int, error) {
	var count int
	statuses := sessionstate.ActiveStatuses()
	placeholders := sqlPlaceholders(len(statuses))
	args := make([]any, len(statuses))
	for i, status := range statuses {
		args[i] = status
	}
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sessions
WHERE status IN (`+placeholders+`)`, args...).Scan(&count)
	return count, err
}

// CountSandboxOccupyingSessions counts sessions that currently hold (or are in
// the middle of bringing up) a live sandbox, i.e. sessions that occupy compute
// resources against the concurrent-sandbox ceiling.
//
// This is deliberately narrower than CountActiveSessions: a session that has
// been checkpointed (resource_state 'reserved_checkpointed') has released its
// sandbox compute and is not counted, and neither is a 'created' session that
// has not spun up a sandbox yet nor an idle session whose sandbox was already
// reclaimed. Only the compute-occupying resource states count: 'allocating',
// 'ready', 'live', and 'recreating' (a checkpoint being restored).
func (s *Store) CountSandboxOccupyingSessions(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT s.id)
FROM sessions s
LEFT JOIN runtime_generations g ON g.generation_id = s.active_generation_id
  AND g.session_id = s.id
LEFT JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE (s.status NOT IN ('failed', 'destroyed') AND r.resource_state IN ('allocating', 'ready', 'live', 'recreating'))
 OR EXISTS(SELECT 1 FROM sandbox_environments e WHERE e.session_id=s.id AND (
   (e.sandbox_id IS NOT NULL AND e.observed_state!='paused') OR
   EXISTS(SELECT 1 FROM environment_operations o WHERE o.environment_id=e.environment_id AND o.kind IN ('create','resume') AND o.state IN ('pending','issued','uncertain'))
 ))`).Scan(&count)
	return count, err
}

func (s *Store) UpdateSessionStatus(ctx context.Context, id, status string, restoreMS *int64) error {
	if err := sessionstate.Validate(status); err != nil {
		return err
	}
	now := time.Now().UTC()
	var terminalAt any
	if sessionstate.IsTerminal(status) {
		terminalAt = formatTime(now)
	}
	query := `
UPDATE sessions
SET status = ?, restore_ms = COALESCE(?, restore_ms), updated_at = ?, ended_at = COALESCE(?, ended_at)
WHERE id = ?`
	args := []any{status, restoreMS, formatTime(now), terminalAt, id}
	if !sessionstate.IsTerminal(status) {
		query += ` AND status NOT IN ('failed', 'destroyed')`
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// UpdateSessionTitle sets a session's display title. When custom is true the
// title is marked user-authored (title_custom=1) and future automatic naming
// must not overwrite it. When custom is false (automatic naming) the update is
// applied only if the title has not been user-authored, so a manual rename is
// never clobbered by a later auto-title.
func (s *Store) UpdateSessionTitle(ctx context.Context, id, title string, custom bool) error {
	now := time.Now().UTC()
	query := `
UPDATE sessions
SET title = ?, title_custom = ?, updated_at = ?
WHERE id = ?`
	if !custom {
		query += ` AND title_custom = 0`
	}
	_, err := s.db.ExecContext(ctx, query, title, boolInt(custom), formatTime(now), id)
	return err
}

func (s *Store) DestroySession(ctx context.Context, id string, now time.Time) (DestroySessionResult, error) {
	if strings.TrimSpace(id) == "" {
		return DestroySessionResult{}, fmt.Errorf("session id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DestroySessionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	generationIDs, err := queryStringColumnTx(ctx, tx, `
SELECT generation_id
FROM runtime_generations
WHERE session_id = ?`, id)
	if err != nil {
		return DestroySessionResult{}, err
	}

	nowString := formatTime(now)
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'destroyed',
    checkpoint_path = NULL,
    restore_ms = NULL,
    error_class = 'session_destroyed',
    failure_reason = 'session_destroyed',
    updated_at = ?,
    ended_at = COALESCE(ended_at, ?)
WHERE id = ?`, nowString, nowString, id); err != nil {
		return DestroySessionResult{}, err
	}

	// Deliberately NOT pendingTurnPredicate. Parking says "do not dispatch this
	// yet", not "this turn no longer exists" — and the session is being destroyed,
	// so a parked turn has to be cancelled along with every other one. Skipping it
	// here would leave a queued row behind a session that can never run it.
	if _, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'canceled',
    completed_at = ?,
    completed_by_generation = COALESCE(completed_by_generation, generation_id),
    error_class = 'session_destroyed',
    error = 'session_destroyed',
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE session_id = ?
  AND status IN ('queued', 'leased', 'running')`, nowString, id); err != nil {
		return DestroySessionResult{}, err
	}

	if len(generationIDs) > 0 {
		args := []any{nowString}
		args = appendStringIDs(args, generationIDs)
		if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = COALESCE(error_class, 'session_destroyed'),
    failure_reason = COALESCE(failure_reason, 'session_destroyed'),
    ended_at = COALESCE(ended_at, ?),
    lease_owner = NULL
WHERE generation_id IN (`+sqlPlaceholders(len(generationIDs))+`)
  AND status NOT IN ('failed', 'destroyed')`, args...); err != nil {
			return DestroySessionResult{}, err
		}
		if err := markAllocationsReclaimableTx(ctx, tx, generationIDs); err != nil {
			return DestroySessionResult{}, err
		}
		if err := deleteActiveContextsForGenerationsTx(ctx, tx, generationIDs); err != nil {
			return DestroySessionResult{}, err
		}
	}

	eventID, err := appendEventTx(ctx, tx, AppendEventParams{
		SessionID: id,
		DedupeKey: "session_destroyed:" + id,
		Type:      "session.destroyed",
		Payload: map[string]any{
			"terminal":           true,
			"status":             "destroyed",
			"session_status":     "destroyed",
			"session_updated_at": nowString,
			"updated_at":         nowString,
		},
		Now: now,
	})
	if err != nil {
		return DestroySessionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DestroySessionResult{}, err
	}
	return DestroySessionResult{GenerationIDs: generationIDs, EventID: eventID}, nil
}

// InterruptSession stops the turn the agent is executing and parks the rest of
// the queue, leaving the generation alone.
//
// The sandbox survives a Stop. Interrupt used to retire the generation because
// the code asserted the agent had no interrupt primitive — it does (see the
// bridge's turn-control writer), so the only thing Stop now changes is turn
// state: the running turn goes 'canceled', the queue behind it is parked, and the
// generation stays live for the next prompt. That removes a cold start from every
// Stop, and keeps the workspace the agent had built up.
//
// The caller is responsible for delivering the abort to the sandbox. Order
// matters and is the caller's to get right: deliver first, then commit this. A
// commit-then-deliver ordering would leave the turn marked canceled while the
// agent kept running, and the completion ack for a turn this transaction already
// retired is discarded by CompleteTurn's CAS.
func (s *Store) InterruptSession(ctx context.Context, p InterruptSessionParams) (InterruptSessionResult, error) {
	if strings.TrimSpace(p.SessionID) == "" {
		return InterruptSessionResult{}, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(p.GenerationID) == "" {
		return InterruptSessionResult{}, fmt.Errorf("generation id is required")
	}
	if strings.TrimSpace(p.OwnerUUID) == "" {
		return InterruptSessionResult{}, fmt.Errorf("owner uuid is required")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.ErrorClass) == "" {
		p.ErrorClass = "user_interrupted"
	}
	if strings.TrimSpace(p.Reason) == "" {
		p.Reason = "interrupted by user"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InterruptSessionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, p.OwnerUUID); err != nil {
		return InterruptSessionResult{}, err
	}
	var sessionStatus, activeGeneration string
	if err := tx.QueryRowContext(ctx, `
SELECT status, COALESCE(active_generation_id, '')
FROM sessions WHERE id = ?`, p.SessionID).Scan(&sessionStatus, &activeGeneration); err != nil {
		return InterruptSessionResult{}, err
	}
	if activeGeneration != p.GenerationID {
		return InterruptSessionResult{}, fmt.Errorf("generation is no longer active")
	}
	// running_idle is accepted, not just running_active. After a Stop the session
	// is idle with its queue parked, and a second press must be a no-op rather than
	// an error: Stop is offered from the coarse session status until the first
	// projection arrives, so a double-press is reachable and routine. Only a
	// terminal or transitional session is refused — those have their own lifecycle
	// to finish and no turn for Stop to act on.
	if sessionStatus != string(sessionstate.RunningActive) && sessionStatus != string(sessionstate.RunningIdle) {
		return InterruptSessionResult{}, fmt.Errorf("session is not running")
	}
	now := formatTime(p.Now)
	// The turn the agent was actually executing. Its message stays in the
	// transcript: the agent did see it, and whatever it streamed before the abort
	// is real work the user should keep.
	var interruptedTurnID int64
	if err := tx.QueryRowContext(ctx, `
SELECT id FROM turns
WHERE session_id = ?
  AND status IN ('leased','running')
ORDER BY sequence
LIMIT 1`, p.SessionID).Scan(&interruptedTurnID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return InterruptSessionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'canceled',
    completed_at = ?,
    completed_by_generation = COALESCE(completed_by_generation, generation_id),
    error_class = ?,
    error = ?,
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE session_id = ?
  AND status IN ('leased','running')`, now, p.ErrorClass, p.Reason, p.SessionID); err != nil {
		return InterruptSessionResult{}, err
	}
	// Turns still queued behind it are parked, not cancelled and not left running.
	//
	// Cancelling them threw away prompts the user was still waiting on, which is
	// not what "Stop" says. Leaving them to run is not it either: the user asked
	// for the work to stop, and having the next prompt start on its own a moment
	// later is the same surprise in slower motion. Parked is the honest third
	// state — the prompts survive, stay visible, and each waits for the user to
	// release it.
	pendingQueued, err := parkQueuedTurnsTx(ctx, tx, p.SessionID)
	if err != nil {
		return InterruptSessionResult{}, err
	}
	// The generation is deliberately untouched. Stop is now a turn-level
	// operation: the agent's abort ends the turn while its process, its session and
	// the workspace all survive, so retiring the generation here would throw away a
	// warm sandbox — and charge the user a cold start — for no reason. The
	// generation's own lifecycle (idle release, checkpointing, failure) is
	// unchanged and still handled by the paths that own it.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM active_model_request_contexts
WHERE session_id = ? AND generation_id = ? AND turn_id = ?`, p.SessionID, p.GenerationID, interruptedTurnID); err != nil {
		return InterruptSessionResult{}, err
	}
	// The generation goes back to idle: the turn that made it active is over, and
	// a parked queue is not pending work. Leaving it 'active' would make the idle
	// sweeper skip it forever, so a session stopped once would hold its sandbox
	// until the session ended.
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle'
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'active'`, p.GenerationID, p.SessionID); err != nil {
		return InterruptSessionResult{}, err
	}
	// Idle, always: after a Stop nothing will run without the user acting again.
	// This is the one place the parked/queued distinction changes the answer — with
	// an auto-running queue this had to report running_active, because going idle
	// with live work would have let the idle sweeper checkpoint a session that
	// still owed a reply. Parked turns owe nothing until released.
	nextStatus := string(sessionstate.RunningIdle)
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    restore_ms = NULL,
    error_class = NULL,
    failure_reason = NULL
WHERE id = ? AND active_generation_id = ? AND status = 'running_active'`, nextStatus, now, p.SessionID, p.GenerationID); err != nil {
		return InterruptSessionResult{}, err
	}
	// A press that found nothing to do says nothing. This is the double-press case
	// accepted above: no turn was executing and the queue was already parked, so
	// there is no state change to announce and an event would only make every
	// client redraw an unchanged session.
	if interruptedTurnID == 0 && pendingQueued == 0 {
		snapshot, err := getSessionRuntimeSnapshot(ctx, tx, p.SessionID)
		if err != nil {
			return InterruptSessionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return InterruptSessionResult{}, err
		}
		// Read from the snapshot rather than the status loaded at the top: the
		// sessions UPDATE above still applies here, and it is worth applying — a
		// running_active session with no turn at all is a stale marker, and settling
		// it to idle is a repair, not a no-op.
		return InterruptSessionResult{
			SessionStatus: snapshot.SessionStatus,
			PendingQueued: 0,
			Runtime:       snapshot,
		}, nil
	}
	// turn.interrupted, not generation.error: the generation is fine. Emitting a
	// generation error here would tell every client the sandbox had failed, and the
	// dedupe key was per-generation, so a second Stop in the same generation
	// silently produced no event at all.
	eventID, err := appendEventTx(ctx, tx, AppendEventParams{
		SessionID:    p.SessionID,
		GenerationID: p.GenerationID,
		DedupeKey:    fmt.Sprintf("turn_interrupted:%s:%d", p.GenerationID, interruptedTurnID),
		Type:         "turn.interrupted",
		Payload: map[string]any{
			"terminal":             false,
			"error_class":          p.ErrorClass,
			"error":                p.Reason,
			"generation_id":        p.GenerationID,
			"turn_id":              interruptedTurnID,
			"parked_queued":        pendingQueued,
			"session_status":       nextStatus,
			"session_updated_at":   now,
			"active_generation_id": p.GenerationID,
		},
		Now: p.Now,
	})
	if err != nil {
		return InterruptSessionResult{}, err
	}
	snapshot, err := getSessionRuntimeSnapshot(ctx, tx, p.SessionID)
	if err != nil {
		return InterruptSessionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return InterruptSessionResult{}, err
	}
	return InterruptSessionResult{
		SessionStatus:     nextStatus,
		EventIDs:          []int64{eventID},
		PendingQueued:     pendingQueued,
		InterruptedTurnID: interruptedTurnID,
		Runtime:           snapshot,
	}, nil
}

func (s *Store) CancelTerminalSessionPendingTurns(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE turns
SET status = 'canceled',
    completed_at = ?,
    completed_by_generation = COALESCE(completed_by_generation, generation_id),
    error_class = COALESCE(
      (SELECT NULLIF(sessions.error_class, '') FROM sessions WHERE sessions.id = turns.session_id),
      CASE (SELECT sessions.status FROM sessions WHERE sessions.id = turns.session_id)
        WHEN 'destroyed' THEN 'session_destroyed'
        ELSE 'session_failed'
      END
    ),
    error = COALESCE(
      (SELECT NULLIF(sessions.failure_reason, '') FROM sessions WHERE sessions.id = turns.session_id),
      CASE (SELECT sessions.status FROM sessions WHERE sessions.id = turns.session_id)
        WHEN 'destroyed' THEN 'session_destroyed'
        ELSE 'session_failed'
      END
    ),
    lease_owner = NULL,
    lease_expires_at = NULL
-- Deliberately NOT pendingTurnPredicate: same reason as DestroySession above.
-- The owning session is already failed or destroyed, so a parked turn is as dead
-- as a queued one and must be retired too.
WHERE status IN ('queued', 'leased', 'running')
  AND EXISTS (
    SELECT 1
    FROM sessions
    WHERE sessions.id = turns.session_id
      AND sessions.status IN ('failed', 'destroyed')
  )`, formatTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) UpdateSessionActiveGeneration(ctx context.Context, p SessionActiveGenerationCASParams) error {
	if strings.TrimSpace(p.SessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(p.NextGenerationID) == "" {
		return fmt.Errorf("next generation id is required")
	}

	args := []any{p.NextGenerationID, p.SessionID}
	where := "active_generation_id IS NULL"
	if p.ExpectedGenerationID.Valid {
		where = "active_generation_id = ?"
		args = append(args, p.ExpectedGenerationID.String)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE sessions
SET active_generation_id = ?
WHERE id = ?
  AND `+where, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("session active generation CAS failed")
	}
	return nil
}

func (s *Store) AddMessage(ctx context.Context, sessionID, role, content string) (Message, error) {
	return s.AddAssistantMessage(ctx, sessionID, role, content, "")
}

// AddAssistantMessage is AddMessage with pi's stop_reason recorded. Callers
// that have the terminator (the pi normalizer's message_end path) use this; all
// others go through AddMessage, which passes an empty stop_reason.
func (s *Store) AddAssistantMessage(ctx context.Context, sessionID, role, content, stopReason string) (Message, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, created_at, stop_reason)
VALUES (?, ?, ?, ?, ?)`, sessionID, role, content, formatTime(now), stopReason)
	if err != nil {
		return Message{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Message{}, err
	}
	return Message{ID: id, SessionID: sessionID, Role: role, Content: content, CreatedAt: now, StopReason: stopReason}, nil
}

func (s *Store) EnqueueTurnMessage(ctx context.Context, p EnqueueTurnMessageParams) (EnqueueTurnMessageResult, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingStatus, activeGenerationID string
	if err := tx.QueryRowContext(ctx, `SELECT status, COALESCE(active_generation_id, '') FROM sessions WHERE id = ?`, p.SessionID).Scan(&existingStatus, &activeGenerationID); err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	// Queue presentation is derived from turn ownership, never from the coarse
	// session status. A session can remain running_active while its generation
	// is warming up (with no turn yet), and conversely can be running_idle while
	// a queued turn is waiting for dispatch. The first in-flight turn therefore
	// belongs in the transcript immediately; only messages appended behind an
	// existing queued/leased/running turn are marked queued.
	var inflightTurns int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM turns
WHERE session_id = ?
  AND `+pendingTurnPredicate+``, p.SessionID).Scan(&inflightTurns); err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	// running_active without a generation is a stale session marker. Preserve
	// the input guard in that case; a legitimate active session always has a
	// generation lease (or an already in-flight turn) by the time input arrives.
	if existingStatus == string(sessionstate.RunningActive) && activeGenerationID == "" && inflightTurns == 0 {
		return EnqueueTurnMessageResult{}, fmt.Errorf("session cannot accept input")
	}
	next, err := nextTurnSequence(ctx, tx, p.SessionID)
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	// materializes_message marks a turn that becomes a transcript entry when it is
	// dispatched. ResumeFailedTurn leaves it 0: the user did not type a
	// continuation, so it must never appear as their message.
	turnRes, err := tx.ExecContext(ctx, `
INSERT INTO turns (session_id, sequence, role, content, status, attempt, created_at, materializes_message)
VALUES (?, ?, 'user', ?, 'queued', 0, ?, 1)`, p.SessionID, next, p.Content, formatTime(p.Now))
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	turnID, err := turnRes.LastInsertId()
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	// No `messages` row here, deliberately. A message row means "this was asked",
	// and a queued turn has not been asked yet — the turn row already records the
	// user's intent, which is what the queue renders.
	//
	// Writing it at enqueue caused two defects. Its created_at claimed the user
	// asked at send time even when the turn then sat in the queue for minutes, and
	// it entered the FTS index immediately, so a prompt that never reached the agent
	// was already searchable. It also had to be filtered out of every read path —
	// and the SSE broadcast was missed, so a queued turn appeared in the transcript
	// and the queue at the same time.
	//
	// MaterializeTurnMessage writes it when the agent actually starts the turn.
	res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    last_activity_at = ?
WHERE id = ?
	AND status IN (?, ?, ?, ?)`,
		string(sessionstate.RunningActive), formatTime(p.Now), formatTime(p.Now), p.SessionID,
		string(sessionstate.Created), string(sessionstate.RunningIdle), string(sessionstate.Checkpointed), string(sessionstate.RunningActive))
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	if affected != 1 {
		return EnqueueTurnMessageResult{}, fmt.Errorf("session cannot accept input")
	}
	// Taken inside the transaction so the caller can hand the client its post-send
	// truth in the POST response. The turns INSERT above already advanced
	// turn_revision via trigger, so this snapshot carries the new value. Without
	// it the client would have to follow every send with a GET, and that GET can
	// race the worker claiming the turn.
	snapshot, err := getSessionRuntimeSnapshot(ctx, tx, p.SessionID)
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	return EnqueueTurnMessageResult{
		TurnID:  turnID,
		Runtime: snapshot,
	}, nil
}

// MaterializeTurnMessage writes the transcript entry for a turn the agent has
// just started, and is the moment a user prompt becomes a message.
//
// Called from AckTurnStarted, immediately before the agent builds its first model
// request for the turn, so `created_at` records when the prompt actually reached
// the model rather than when it was typed.
//
// Idempotent: the bridge can replay an ack, so an existing row is returned as-is
// (ok=false) instead of duplicating the message. Turns with
// materializes_message = 0 (automatic continuations) never produce a row.
func materializeTurnMessageTx(ctx context.Context, tx *sql.Tx, sessionID string, turnID int64, now time.Time) (Message, bool, error) {
	var (
		content      string
		materializes int
	)
	err := tx.QueryRowContext(ctx, `
SELECT content, materializes_message
FROM turns
WHERE id = ? AND session_id = ?`, turnID, sessionID).Scan(&content, &materializes)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	if materializes != 1 {
		return Message{}, false, nil
	}
	// A replayed ack must not append the prompt twice.
	var existing int64
	err = tx.QueryRowContext(ctx, `
SELECT id FROM messages WHERE session_id = ? AND turn_id = ? AND role = 'user'`, sessionID, turnID).Scan(&existing)
	if err == nil {
		return Message{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, err
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, created_at, turn_id)
VALUES (?, 'user', ?, ?, ?)`, sessionID, content, formatTime(now), turnID)
	if err != nil {
		return Message{}, false, err
	}
	messageID, err := res.LastInsertId()
	if err != nil {
		return Message{}, false, err
	}
	return Message{
		ID:        messageID,
		SessionID: sessionID,
		TurnID:    &turnID,
		Role:      "user",
		Content:   content,
		CreatedAt: now,
	}, true, nil
}

// TurnUserMessage returns the transcript entry a dispatched turn produced, or
// ok=false when it has none (an automatic continuation, or a turn that has not
// been dispatched yet). Lets the bridge path publish the message that
// AckTurnStarted committed without threading it back through the processor.
func (s *Store) TurnUserMessage(ctx context.Context, sessionID string, turnID int64) (Message, bool, error) {
	var (
		m         Message
		createdAt string
	)
	err := s.reader().QueryRowContext(ctx, `
SELECT id, session_id, turn_id, role, content, created_at
FROM messages
WHERE session_id = ? AND turn_id = ? AND role = 'user'`, sessionID, turnID).Scan(
		&m.ID, &m.SessionID, &turnID, &m.Role, &m.Content, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	m.TurnID = &turnID
	m.CreatedAt = parseTime(createdAt)
	return m, true, nil
}

func (s *Store) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	return s.listMessages(ctx, sessionID, "")
}

// ListMessagesForUser is ListMessages plus the given user's thumbs rating on
// each message. An empty userID behaves like ListMessages (no feedback joined).
func (s *Store) ListMessagesForUser(ctx context.Context, sessionID, userID string) ([]Message, error) {
	return s.listMessages(ctx, sessionID, userID)
}

func (s *Store) listMessages(ctx context.Context, sessionID, userID string) ([]Message, error) {
	// LEFT JOIN the trace (one row per message) and, when a user is known, that
	// user's feedback. Both are optional so history predating either table still
	// reads back cleanly as NULL.
	rows, err := s.reader().QueryContext(ctx, `
SELECT m.id, m.session_id, m.turn_id, m.role, m.content, m.created_at, m.stop_reason, t.nodes, f.rating
FROM messages m
LEFT JOIN message_traces t ON t.message_id = m.id
LEFT JOIN message_feedback f ON f.message_id = m.id AND f.user_id = ?
-- No turn-status filter, and none is needed: a message row exists only once its
-- turn was dispatched (materializeTurnMessageTx), so a queued turn has nothing
-- here to exclude. Earlier revisions filtered on turns.status — first with a
-- correlated "except the head of the queue" subquery — because the row was written
-- at enqueue. Every read path then had to remember the filter, and the SSE
-- broadcast did not, which is how a queued prompt ended up in the transcript and
-- the queue at the same time.
WHERE m.session_id = ?
ORDER BY m.id ASC`, userID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		var m Message
		var createdAt string
		var stopReason sql.NullString
		var nodes sql.NullString
		var rating sql.NullString
		var turnID sql.NullInt64
		if err := rows.Scan(&m.ID, &m.SessionID, &turnID, &m.Role, &m.Content, &createdAt, &stopReason, &nodes, &rating); err != nil {
			return nil, err
		}
		if turnID.Valid {
			m.TurnID = &turnID.Int64
		}
		m.CreatedAt = parseTime(createdAt)
		if stopReason.Valid {
			m.StopReason = stopReason.String
		}
		if nodes.Valid && nodes.String != "" {
			m.Trace = json.RawMessage(nodes.String)
		}
		if rating.Valid {
			m.Feedback = rating.String
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// SaveMessageTrace stores (or replaces) the execution-trace node array for an
// assistant message. nodes must be a JSON array; an empty/blank value is a
// no-op so callers can pass through turns that produced no trace.
func (s *Store) SaveMessageTrace(ctx context.Context, messageID int64, sessionID string, nodes json.RawMessage) error {
	if len(nodes) == 0 || strings.TrimSpace(string(nodes)) == "" || string(nodes) == "[]" {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO message_traces (message_id, session_id, nodes, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(message_id) DO UPDATE SET nodes = excluded.nodes`,
		messageID, sessionID, string(nodes), formatTime(now))
	return err
}

// SetMessageFeedback upserts the user's thumbs rating for a message. rating must
// be "up" or "down"; passing "" (or "none") clears any existing rating. Returns
// the effective rating after the operation ("" when cleared).
func (s *Store) SetMessageFeedback(ctx context.Context, messageID int64, userID, rating string) (string, error) {
	rating = strings.TrimSpace(rating)
	if rating == "" || rating == "none" {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM message_feedback WHERE message_id = ? AND user_id = ?`, messageID, userID); err != nil {
			return "", err
		}
		return "", nil
	}
	if rating != "up" && rating != "down" {
		return "", fmt.Errorf("invalid feedback rating %q", rating)
	}
	now := formatTime(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO message_feedback (message_id, user_id, rating, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(message_id, user_id) DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at`,
		messageID, userID, rating, now, now); err != nil {
		return "", err
	}
	return rating, nil
}

// MessageSession returns the session id that owns a message, or ("", false)
// when no such message exists. Used to authorize feedback writes.
func (s *Store) MessageSession(ctx context.Context, messageID int64) (string, bool, error) {
	var sessionID string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM messages WHERE id = ?`, messageID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sessionID, true, nil
}

func (s *Store) UpsertArtifact(ctx context.Context, artifact Artifact) error {
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}
	if artifact.UpdatedAt.IsZero() {
		artifact.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO artifacts (session_id, path, size, mod_time, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, path) DO UPDATE SET
  size = excluded.size,
  mod_time = excluded.mod_time,
  updated_at = excluded.updated_at`,
		artifact.SessionID, artifact.Path, artifact.Size, formatTime(artifact.ModTime),
		formatTime(artifact.CreatedAt), formatTime(artifact.UpdatedAt),
	)
	return err
}

func (s *Store) ListArtifacts(ctx context.Context, sessionID string) ([]Artifact, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT session_id, path, size, mod_time, created_at, updated_at
FROM artifacts WHERE session_id = ? ORDER BY path`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	artifacts := []Artifact{}
	for rows.Next() {
		var artifact Artifact
		var modTime, createdAt, updatedAt string
		if err := rows.Scan(&artifact.SessionID, &artifact.Path, &artifact.Size, &modTime, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		artifact.ModTime = parseTime(modTime)
		artifact.CreatedAt = parseTime(createdAt)
		artifact.UpdatedAt = parseTime(updatedAt)
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func (s *Store) DeleteArtifactPath(ctx context.Context, sessionID, artifactPath string) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM artifacts
WHERE session_id = ?
  AND (path = ? OR path LIKE ? ESCAPE '\')`,
		sessionID, artifactPath, escapeLike(artifactPath)+"/%",
	)
	return err
}

// rowScanner is the shared abstraction over *sql.Row and *sql.Rows used by the
// package's scan helpers. Aliased to the sqlitex definition so a helper can be
// passed between the control and memory stores without conversion.
type rowScanner = sqlitex.RowScanner

// sessionScan holds the nullable landing spots for the sessionSelectColumns
// projection. It exists so a query that selects session columns PLUS extra
// columns (see ListSessionsSharedWith) can reuse the one canonical scan instead
// of duplicating the 21-column mapping: dest() supplies the targets to append
// to, and finish() folds them into the Session.
type sessionScan struct {
	session               Session
	restoreMS             sql.NullInt64
	agentHomePath         sql.NullString
	activeGenerationID    sql.NullString
	createdAt             string
	updatedAt             string
	expiresAt             sql.NullString
	endedAt               sql.NullString
	lastActivityAt        sql.NullString
	checkpointPath        sql.NullString
	failureReason         sql.NullString
	errorClass            sql.NullString
	autoCheckpointEnabled int
	titleCustom           int
}

func (sc *sessionScan) dest() []any {
	return []any{
		&sc.session.ID, &sc.session.UserID, &sc.session.Status, &sc.session.Agent, &sc.session.Mode,
		&sc.session.Workspace, &sc.agentHomePath, &sc.activeGenerationID, &sc.session.RestoreID,
		&sc.restoreMS, &sc.createdAt, &sc.updatedAt, &sc.expiresAt, &sc.endedAt, &sc.lastActivityAt,
		&sc.checkpointPath, &sc.autoCheckpointEnabled, &sc.failureReason, &sc.errorClass,
		&sc.session.Title, &sc.titleCustom,
	}
}

func (sc *sessionScan) finish() Session {
	session := sc.session
	if sc.restoreMS.Valid {
		session.RestoreMS = &sc.restoreMS.Int64
	}
	if sc.agentHomePath.Valid {
		session.AgentHomePath = sc.agentHomePath.String
	}
	if sc.activeGenerationID.Valid {
		session.ActiveGenerationID = sc.activeGenerationID.String
	}
	session.CreatedAt = parseTime(sc.createdAt)
	session.UpdatedAt = parseTime(sc.updatedAt)
	session.ExpiresAt = optionalTimePtr(sc.expiresAt)
	session.EndedAt = optionalTimePtr(sc.endedAt)
	session.LastActivityAt = optionalTimePtr(sc.lastActivityAt)
	if sc.checkpointPath.Valid {
		session.CheckpointPath = sc.checkpointPath.String
	}
	session.AutoCheckpointEnabled = sc.autoCheckpointEnabled != 0
	if sc.failureReason.Valid {
		session.FailureReason = sc.failureReason.String
	}
	if sc.errorClass.Valid {
		session.ErrorClass = sc.errorClass.String
	}
	session.TitleCustom = sc.titleCustom != 0
	if strings.TrimSpace(session.Mode) == "" {
		session.Mode = ModeForDriver(session.Agent)
	}
	return session
}

func scanSession(row rowScanner) (Session, error) {
	var sc sessionScan
	err := row.Scan(sc.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	if err != nil {
		return Session{}, err
	}
	return sc.finish(), nil
}

func ModeForDriver(driverID string) string {
	spec, ok := agents.DriverSpecFor(driverID)
	if !ok {
		return ""
	}
	switch spec.Kind {
	case agents.DriverKindAgent, agents.DriverKindShell:
		return string(spec.Kind)
	default:
		return ""
	}
}

// The time/null/placeholder helpers below are the package-local spelling of the
// shared sqlitex primitives. They stay as unexported aliases so the ~137 call
// sites across this package keep reading the way they always have, while the
// single implementation is shared with internal/memorystore.
var (
	nullableString     = sqlitex.NullableString
	formatOptionalTime = sqlitex.FormatOptionalTime
	formatTime         = sqlitex.FormatTime
	parseTime          = sqlitex.ParseTime
	optionalTimePtr    = sqlitex.OptionalTimePtr
	sqlPlaceholders    = sqlitex.Placeholders
	escapeLike         = sqlitex.EscapeLike
)

func formatEventTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func (s *Store) UpdateSessionStatusAndActivity(ctx context.Context, id, status string, restoreMS *int64, lastActivity time.Time) error {
	if err := sessionstate.Validate(status); err != nil {
		return err
	}
	now := time.Now().UTC()
	var terminalAt any
	if sessionstate.IsTerminal(status) {
		terminalAt = formatTime(now)
	}
	query := `
UPDATE sessions
SET status = ?, restore_ms = COALESCE(?, restore_ms), updated_at = ?, ended_at = COALESCE(?, ended_at), last_activity_at = ?
WHERE id = ?`
	args := []any{status, restoreMS, formatTime(now), terminalAt, formatTime(lastActivity), id}
	if !sessionstate.IsTerminal(status) {
		query += ` AND status NOT IN ('failed', 'destroyed')`
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

type FailSessionParams struct {
	SessionID    string
	ErrorClass   string
	Reason       string
	LastActivity time.Time
	Now          time.Time
}

func (s *Store) FailSession(ctx context.Context, p FailSessionParams) error {
	if strings.TrimSpace(p.SessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lastActivity := p.LastActivity
	if lastActivity.IsZero() {
		lastActivity = now
	}
	nowString := formatTime(now)
	_, err := s.db.ExecContext(ctx, `
UPDATE sessions
SET status = 'failed',
    updated_at = ?,
    ended_at = COALESCE(ended_at, ?),
    last_activity_at = ?,
    error_class = ?,
    failure_reason = ?
WHERE id = ?
  AND status != 'destroyed'`,
		nowString,
		nowString,
		formatTime(lastActivity),
		nullableString(p.ErrorClass),
		nullableString(p.Reason),
		p.SessionID,
	)
	return err
}

func (s *Store) ListSessionsByStatus(ctx context.Context, status string) ([]Session, error) {
	if err := sessionstate.Validate(status); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+sessionSelectColumns+`
FROM sessions WHERE status = ? ORDER BY last_activity_at ASC`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func sqlStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", "''"))
	}
	return strings.Join(quoted, ",")
}
