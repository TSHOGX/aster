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

type Message struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
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
}

type EnqueueTurnMessageParams struct {
	SessionID string
	Content   string
	Now       time.Time
}

type EnqueueTurnMessageResult struct {
	TurnID  int64
	Message Message
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

	writer, err := sqlitex.OpenWriter(path)
	if err != nil {
		return nil, err
	}

	store := &Store{db: writer, options: options}
	// The writer creates the file and owns schema changes. Migrating before
	// the reader pool exists means no reader can observe a half-built schema.
	if err := store.migrate(ctx); err != nil {
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

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	if err := sessionstate.Validate(session.Status); err != nil {
		return err
	}
	if _, ok := agents.Lookup(session.Agent); !ok {
		return fmt.Errorf("unsupported driver %q", session.Agent)
	}
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

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+sessionSelectColumns+`
FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
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
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
  AND g.session_id = s.id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE s.status NOT IN ('failed', 'destroyed')
  AND r.resource_state IN ('allocating', 'ready', 'live', 'recreating')`).Scan(&count)
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

// AlignAutoCheckpointEnabled reconciles the per-session auto_checkpoint flag
// (and the flag on each session's active generation) with the process-wide
// configured default. The flag is otherwise snapshotted only at session-create
// time, so sessions created before a config change would keep the old value
// forever. Calling this at startup lets an operator flip
// harness.checkpoint.auto_enabled and have it take effect on existing
// sessions after a restart.
//
// Only non-terminal sessions that are not mid-turn are touched: busy states
// (running_active, checkpointing) are left alone so we never race an
// in-flight checkpoint or turn. Returns the number of sessions updated.
func (s *Store) AlignAutoCheckpointEnabled(ctx context.Context, enabled bool) (int64, error) {
	flag := boolInt(enabled)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET auto_checkpoint_enabled = ?
WHERE status IN ('created', 'running_idle', 'checkpointed')
  AND auto_checkpoint_enabled <> ?`, flag, flag)
	if err != nil {
		return 0, err
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET auto_checkpoint_enabled = ?
WHERE auto_checkpoint_enabled <> ?
  AND generation_id IN (
    SELECT active_generation_id FROM sessions
    WHERE status IN ('created', 'running_idle', 'checkpointed')
      AND active_generation_id IS NOT NULL
  )`, flag, flag); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
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
	next, err := nextTurnSequence(ctx, tx, p.SessionID)
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	turnRes, err := tx.ExecContext(ctx, `
INSERT INTO turns (session_id, sequence, role, content, status, attempt, created_at)
VALUES (?, ?, 'user', ?, 'queued', 0, ?)`, p.SessionID, next, p.Content, formatTime(p.Now))
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	turnID, err := turnRes.LastInsertId()
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	msgRes, err := tx.ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, created_at)
VALUES (?, 'user', ?, ?)`, p.SessionID, p.Content, formatTime(p.Now))
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	messageID, err := msgRes.LastInsertId()
	if err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    last_activity_at = ?
WHERE id = ?
  AND status IN (?, ?, ?)`,
		string(sessionstate.RunningActive), formatTime(p.Now), formatTime(p.Now), p.SessionID,
		string(sessionstate.Created), string(sessionstate.RunningIdle), string(sessionstate.Checkpointed))
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
	if err := tx.Commit(); err != nil {
		return EnqueueTurnMessageResult{}, err
	}
	return EnqueueTurnMessageResult{
		TurnID: turnID,
		Message: Message{
			ID:        messageID,
			SessionID: p.SessionID,
			Role:      "user",
			Content:   p.Content,
			CreatedAt: p.Now,
		},
	}, nil
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
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.session_id, m.role, m.content, m.created_at, m.stop_reason, t.nodes, f.rating
FROM messages m
LEFT JOIN message_traces t ON t.message_id = m.id
LEFT JOIN message_feedback f ON f.message_id = m.id AND f.user_id = ?
WHERE m.session_id = ? ORDER BY m.id ASC`, userID, sessionID)
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
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &createdAt, &stopReason, &nodes, &rating); err != nil {
			return nil, err
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
	rows, err := s.db.QueryContext(ctx, `
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

func scanSession(row rowScanner) (Session, error) {
	var session Session
	var restoreMS sql.NullInt64
	var agentHomePath, activeGenerationID sql.NullString
	var createdAt, updatedAt string
	var expiresAt, endedAt, lastActivityAt sql.NullString
	var checkpointPath, failureReason, errorClass sql.NullString
	var autoCheckpointEnabled int
	var titleCustom int
	err := row.Scan(
		&session.ID, &session.UserID, &session.Status, &session.Agent, &session.Mode, &session.Workspace, &agentHomePath,
		&activeGenerationID, &session.RestoreID, &restoreMS, &createdAt, &updatedAt,
		&expiresAt, &endedAt, &lastActivityAt, &checkpointPath, &autoCheckpointEnabled, &failureReason, &errorClass,
		&session.Title, &titleCustom,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	if err != nil {
		return Session{}, err
	}
	if restoreMS.Valid {
		session.RestoreMS = &restoreMS.Int64
	}
	if agentHomePath.Valid {
		session.AgentHomePath = agentHomePath.String
	}
	if activeGenerationID.Valid {
		session.ActiveGenerationID = activeGenerationID.String
	}
	session.CreatedAt = parseTime(createdAt)
	session.UpdatedAt = parseTime(updatedAt)
	if expiresAt.Valid {
		t := parseTime(expiresAt.String)
		session.ExpiresAt = &t
	}
	if endedAt.Valid {
		t := parseTime(endedAt.String)
		session.EndedAt = &t
	}
	if lastActivityAt.Valid {
		t := parseTime(lastActivityAt.String)
		session.LastActivityAt = &t
	}
	if checkpointPath.Valid {
		session.CheckpointPath = checkpointPath.String
	}
	session.AutoCheckpointEnabled = autoCheckpointEnabled != 0
	if failureReason.Valid {
		session.FailureReason = failureReason.String
	}
	if errorClass.Valid {
		session.ErrorClass = errorClass.String
	}
	session.TitleCustom = titleCustom != 0
	if strings.TrimSpace(session.Mode) == "" {
		session.Mode = ModeForDriver(session.Agent)
	}
	return session, nil
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
