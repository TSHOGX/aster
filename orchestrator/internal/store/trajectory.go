package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// Trajectory distillation store layer (agents-kb pipeline).
//
// Two lifecycles share this file, mirroring the schema's table groups:
//
//   * REBUILDABLE view  — TrajectorySnapshot + TrajectoryEvent, derived
//     mechanically from messages/message_traces by SnapshotSession. A
//     re-snapshot replaces a session's events wholesale (DELETE + batch INSERT)
//     and never touches the distilled tables below.
//   * NON-REBUILDABLE output — EpisodeMemory, SemanticMemory, SkillCandidate.
//     Expensive LLM output keyed by session/evidence, joined to the mechanical
//     half only by soft keys.
//
// See internal/store/schema.go ("Trajectory distillation") for the tables.
// ============================================================================

// TrajectorySnapshot is one index row per snapshotted session. fingerprint =
// turn_count + last message id/created_at; the pipeline only re-normalizes and
// re-extracts a session whose fingerprint moved since the last run.
type TrajectorySnapshot struct {
	SessionID    string     `json:"session_id"`
	Status       string     `json:"status"`
	Agent        string     `json:"agent"`
	UserID       string     `json:"user_id"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	TurnCount    int        `json:"turn_count"`
	MessageCount int        `json:"message_count"`
	Fingerprint  string     `json:"fingerprint"`
	Fidelity     string     `json:"fidelity"`
	NormalizedAt *time.Time `json:"normalized_at,omitempty"`
	ExtractedAt  *time.Time `json:"extracted_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// TrajectoryEvent is one entry in a session's normalized event stream, using the
// 7-primitive taxonomy (user/assistant/thinking/tool_call/tool_result/system/
// usage). Derived from messages.role and message_traces.nodes[].
type TrajectoryEvent struct {
	SessionID string `json:"session_id"`
	Idx       int    `json:"idx"`
	Kind      string `json:"kind"`
	Subtype   string `json:"subtype,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	IsError   bool   `json:"is_error"`
	// NativeID/ParentID carry pi's native intra-session lineage (Tree A): the
	// record's own id and its parentId. Empty for events derived from the legacy
	// messages/message_traces projection.
	NativeID  string     `json:"native_id,omitempty"`
	ParentID  string     `json:"parent_id,omitempty"`
	MessageID *int64     `json:"message_id,omitempty"`
	Tokens    *int64     `json:"tokens,omitempty"`
	Text      string     `json:"text"`
	TS        *time.Time `json:"ts,omitempty"`
}

// EpisodeMemory is a structured summary of one session (ReasoningBank episodic
// item). Candidate memories live in CandidateMemories until consolidation lifts
// them into semantic_memories.
type EpisodeMemory struct {
	ID                int64           `json:"id"`
	SessionID         string          `json:"session_id"`
	Task              string          `json:"task"`
	Outcome           string          `json:"outcome"`
	Domains           []string        `json:"domains"`
	WorkedSteps       json.RawMessage `json:"worked_steps"`
	FailedSteps       json.RawMessage `json:"failed_steps"`
	UserFrustration   string          `json:"user_frustration"`
	CandidateMemories json.RawMessage `json:"candidate_memories"`
	Evidence          json.RawMessage `json:"evidence"`
	Model             string          `json:"model"`
	CreatedAt         time.Time       `json:"created_at"`
}

// SemanticMemory is cross-session consolidated knowledge. support/contradiction
// counts drive confidence; status gates promotion into the shared KB.
type SemanticMemory struct {
	ID                 int64     `json:"id"`
	Type               string    `json:"type"`
	Scope              string    `json:"scope"`
	Statement          string    `json:"statement"`
	WhenToApply        string    `json:"when_to_apply"`
	SupportCount       int       `json:"support_count"`
	ContradictionCount int       `json:"contradiction_count"`
	EvidenceSessionIDs []string  `json:"evidence_session_ids"`
	Confidence         float64   `json:"confidence"`
	Status             string    `json:"status"`
	FirstSeenAt        time.Time `json:"first_seen_at"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// SkillCandidate is a proposed change to the shared skills pack. body_md is
// human-reviewable Markdown, not opaque model output.
type SkillCandidate struct {
	ID                 int64      `json:"id"`
	TargetSkillName    string     `json:"target_skill_name"`
	Operation          string     `json:"operation"`
	Title              string     `json:"title"`
	BodyMD             string     `json:"body_md"`
	SourceMemoryIDs    []int64    `json:"source_memory_ids"`
	EvidenceSessionIDs []string   `json:"evidence_session_ids"`
	SupportCount       int        `json:"support_count"`
	Confidence         float64    `json:"confidence"`
	Status             string     `json:"status"`
	KBStatus           string     `json:"kb_status"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	PublishedAt        *time.Time `json:"published_at,omitempty"`
}

// ExtractionState is the per-(session, stage) watermark. processed_turn_count
// lets a lens re-process a session only after it has grown. Stages are the lens
// families: 'facts' | 'failure' | 'skill' | 'curator'.
type ExtractionState struct {
	SessionID          string     `json:"session_id"`
	Stage              string     `json:"stage"`
	ProcessedTurnCount int        `json:"processed_turn_count"`
	ProcessedAt        *time.Time `json:"processed_at,omitempty"`
	Status             string     `json:"status"`
	Error              string     `json:"error,omitempty"`
}

// trajTraceNode is a local mirror of internal/server.traceNode. We redeclare it
// here rather than import the server package: server imports store, so importing
// server back would create a cycle. Only the fields the normalizer needs are
// kept; the JSON tags must stay in lockstep with trace_collector.go.
type trajTraceNode struct {
	Kind          string          `json:"kind"` // "thinking" | "tool"
	ToolName      string          `json:"toolName,omitempty"`
	Label         string          `json:"label,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
	ResultSummary string          `json:"resultSummary,omitempty"`
	IsError       bool            `json:"isError,omitempty"`
	StartedAt     string          `json:"startedAt,omitempty"`
	DurationMs    int64           `json:"durationMs,omitempty"`
}

// SnapshotSession normalizes a session's durable messages/message_traces into
// the rebuildable trajectory_events stream and upserts its trajectory_snapshots
// index row. It never touches the distilled memory/skill tables.
//
// Normalization applies the 7-primitive taxonomy: each user message becomes one
// "user" event; each assistant message first emits its trace nodes in order (a
// thinking node -> one "thinking" event; a tool node -> a "tool_call" then a
// "tool_result" event sharing a synthesized call_id) then the assistant text as
// an "assistant" event. idx is a 0-based running counter across the session.
func (s *Store) SnapshotSession(ctx context.Context, sessionID string) error {
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("snapshot session %q: load session: %w", sessionID, err)
	}
	messages, err := s.listMessages(ctx, sessionID, "")
	if err != nil {
		return fmt.Errorf("snapshot session %q: list messages: %w", sessionID, err)
	}

	var turnCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM turns WHERE session_id = ?`, sessionID).Scan(&turnCount); err != nil {
		return fmt.Errorf("snapshot session %q: count turns: %w", sessionID, err)
	}

	events, err := normalizeEvents(sessionID, messages)
	if err != nil {
		return fmt.Errorf("snapshot session %q: normalize: %w", sessionID, err)
	}

	fingerprint := trajectoryFingerprint(turnCount, messages)
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("snapshot session %q: begin tx: %w", sessionID, err)
	}
	defer func() { _ = tx.Rollback() }()

	// Rebuildable half: wholesale replace the event stream.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM trajectory_events WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("snapshot session %q: clear events: %w", sessionID, err)
	}
	for _, ev := range events {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO trajectory_events (
  session_id, idx, kind, subtype, name, call_id, is_error, message_id, tokens, text, ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.SessionID, ev.Idx, ev.Kind,
			nullableString(ev.Subtype), nullableString(ev.Name), nullableString(ev.CallID),
			boolInt(ev.IsError), ev.MessageID, ev.Tokens, ev.Text, formatOptionalTime(ev.TS),
		); err != nil {
			return fmt.Errorf("snapshot session %q: insert event %d: %w", sessionID, ev.Idx, err)
		}
	}

	// Index row (fidelity=partial: this is the lossy messages/traces projection).
	if err := upsertTrajectorySnapshotTx(ctx, tx, TrajectorySnapshot{
		SessionID:    session.ID,
		Status:       session.Status,
		Agent:        session.Agent,
		UserID:       session.UserID,
		StartedAt:    &session.CreatedAt,
		EndedAt:      session.EndedAt,
		TurnCount:    turnCount,
		MessageCount: len(messages),
		Fingerprint:  fingerprint,
		Fidelity:     "partial",
		NormalizedAt: &now,
	}); err != nil {
		return fmt.Errorf("snapshot session %q: %w", sessionID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("snapshot session %q: commit: %w", sessionID, err)
	}
	return nil
}

// upsertTrajectorySnapshotTx inserts-or-updates the index row for a session,
// preserving created_at and extracted_at across updates (only the normalize-time
// fields and fidelity change). fidelity defaults to 'full' when unset.
func upsertTrajectorySnapshotTx(ctx context.Context, tx *sql.Tx, snap TrajectorySnapshot) error {
	now := time.Now().UTC()
	fidelity := strings.TrimSpace(snap.Fidelity)
	if fidelity == "" {
		fidelity = "full"
	}
	normalizedAt := snap.NormalizedAt
	if normalizedAt == nil {
		normalizedAt = &now
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO trajectory_snapshots (
  session_id, status, agent, user_id, started_at, ended_at,
  turn_count, message_count, fingerprint, fidelity, normalized_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
  status = excluded.status,
  agent = excluded.agent,
  user_id = excluded.user_id,
  started_at = excluded.started_at,
  ended_at = excluded.ended_at,
  turn_count = excluded.turn_count,
  message_count = excluded.message_count,
  fingerprint = excluded.fingerprint,
  fidelity = excluded.fidelity,
  normalized_at = excluded.normalized_at`,
		snap.SessionID, snap.Status, snap.Agent, snap.UserID,
		formatOptionalTime(snap.StartedAt), formatOptionalTime(snap.EndedAt),
		snap.TurnCount, snap.MessageCount, snap.Fingerprint, fidelity, formatTime(*normalizedAt), formatTime(now),
	); err != nil {
		return fmt.Errorf("upsert trajectory snapshot: %w", err)
	}
	return nil
}

// normalizeEvents flattens messages (+ their trace nodes) into the ordered
// 7-primitive event stream. idx is a running 0-based counter across the session.
func normalizeEvents(sessionID string, messages []Message) ([]TrajectoryEvent, error) {
	events := []TrajectoryEvent{}
	idx := 0
	appendEvent := func(ev TrajectoryEvent) {
		ev.SessionID = sessionID
		ev.Idx = idx
		idx++
		events = append(events, ev)
	}

	for _, m := range messages {
		messageID := m.ID
		ts := m.CreatedAt

		if m.Role != "assistant" {
			// user (and any non-assistant role, e.g. shell output) -> one event.
			kind := "user"
			if m.Role != "user" {
				kind = "system"
			}
			appendEvent(TrajectoryEvent{
				Kind:      kind,
				Subtype:   subtypeForRole(m.Role),
				MessageID: &messageID,
				Text:      m.Content,
				TS:        tsPtr(ts),
			})
			continue
		}

		// Assistant: emit trace nodes first, then the assistant text.
		if len(m.Trace) > 0 {
			var nodes []trajTraceNode
			if err := json.Unmarshal(m.Trace, &nodes); err != nil {
				return nil, fmt.Errorf("message %d: parse trace: %w", m.ID, err)
			}
			for nodeIdx, node := range nodes {
				switch node.Kind {
				case "thinking":
					text := node.Label
					if text == "" {
						text = "Thinking"
					}
					appendEvent(TrajectoryEvent{
						Kind:      "thinking",
						MessageID: &messageID,
						Text:      text,
						TS:        tsPtr(parseTraceTime(node.StartedAt, ts)),
					})
				case "tool":
					callID := fmt.Sprintf("%d-%d", m.ID, nodeIdx)
					nodeTS := parseTraceTime(node.StartedAt, ts)
					appendEvent(TrajectoryEvent{
						Kind:      "tool_call",
						Name:      node.ToolName,
						CallID:    callID,
						MessageID: &messageID,
						Text:      string(node.Args),
						TS:        tsPtr(nodeTS),
					})
					appendEvent(TrajectoryEvent{
						Kind:      "tool_result",
						Name:      node.ToolName,
						CallID:    callID,
						IsError:   node.IsError,
						MessageID: &messageID,
						Text:      node.ResultSummary,
						TS:        tsPtr(nodeTS),
					})
				}
			}
		}

		appendEvent(TrajectoryEvent{
			Kind:      "assistant",
			MessageID: &messageID,
			Text:      m.Content,
			TS:        tsPtr(ts),
		})
	}
	return events, nil
}

func subtypeForRole(role string) string {
	if role == "user" || role == "assistant" {
		return ""
	}
	return role
}

// parseTraceTime parses a trace node's RFC3339Nano timestamp, falling back to
// the owning message's created_at when the node has no (or an unparseable) time.
func parseTraceTime(value string, fallback time.Time) time.Time {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	return fallback
}

func tsPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// trajectoryFingerprint is a cheap change token: turn count plus the id and
// created_at of the last message. It moves whenever a session grows, which is
// all the incremental pipeline needs to decide whether to re-process.
func trajectoryFingerprint(turnCount int, messages []Message) string {
	var lastID int64
	var lastAt string
	if n := len(messages); n > 0 {
		lastID = messages[n-1].ID
		lastAt = formatTime(messages[n-1].CreatedAt)
	}
	return fmt.Sprintf("t=%d;m=%d;last=%d@%s", turnCount, len(messages), lastID, lastAt)
}

// GetTrajectoryEvents returns a session's normalized events ordered by idx.
func (s *Store) GetTrajectoryEvents(ctx context.Context, sessionID string) ([]TrajectoryEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT session_id, idx, kind, subtype, name, call_id, is_error, native_id, parent_id, message_id, tokens, text, ts
FROM trajectory_events WHERE session_id = ? ORDER BY idx ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get trajectory events %q: %w", sessionID, err)
	}
	defer rows.Close()

	events := []TrajectoryEvent{}
	for rows.Next() {
		ev, err := scanTrajectoryEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

func scanTrajectoryEvent(sc interface{ Scan(...any) error }) (TrajectoryEvent, error) {
	var ev TrajectoryEvent
	var subtype, name, callID, nativeID, parentID sql.NullString
	var isError int
	var messageID, tokens sql.NullInt64
	var ts sql.NullString
	if err := sc.Scan(&ev.SessionID, &ev.Idx, &ev.Kind, &subtype, &name, &callID,
		&isError, &nativeID, &parentID, &messageID, &tokens, &ev.Text, &ts); err != nil {
		return TrajectoryEvent{}, fmt.Errorf("scan trajectory event: %w", err)
	}
	ev.Subtype = subtype.String
	ev.Name = name.String
	ev.CallID = callID.String
	ev.NativeID = nativeID.String
	ev.ParentID = parentID.String
	ev.IsError = isError != 0
	if messageID.Valid {
		ev.MessageID = &messageID.Int64
	}
	if tokens.Valid {
		ev.Tokens = &tokens.Int64
	}
	if ts.Valid && ts.String != "" {
		t := parseTime(ts.String)
		ev.TS = &t
	}
	return ev, nil
}

// ListSessionsForDistillation returns snapshots that changed or were never
// extracted: ended_at >= since OR extracted_at IS NULL, ordered by ended_at.
func (s *Store) ListSessionsForDistillation(ctx context.Context, since time.Time) ([]TrajectorySnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT session_id, status, agent, user_id, started_at, ended_at,
       turn_count, message_count, fingerprint, fidelity, normalized_at, extracted_at, created_at
FROM trajectory_snapshots
WHERE extracted_at IS NULL OR (ended_at IS NOT NULL AND ended_at >= ?)
ORDER BY ended_at ASC`, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("list sessions for distillation: %w", err)
	}
	defer rows.Close()

	snapshots := []TrajectorySnapshot{}
	for rows.Next() {
		snap, err := scanTrajectorySnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

func scanTrajectorySnapshot(sc interface{ Scan(...any) error }) (TrajectorySnapshot, error) {
	var snap TrajectorySnapshot
	var startedAt, endedAt, normalizedAt, extractedAt sql.NullString
	var createdAt string
	if err := sc.Scan(&snap.SessionID, &snap.Status, &snap.Agent, &snap.UserID,
		&startedAt, &endedAt, &snap.TurnCount, &snap.MessageCount, &snap.Fingerprint,
		&snap.Fidelity, &normalizedAt, &extractedAt, &createdAt); err != nil {
		return TrajectorySnapshot{}, fmt.Errorf("scan trajectory snapshot: %w", err)
	}
	snap.StartedAt = optionalTimePtr(startedAt)
	snap.EndedAt = optionalTimePtr(endedAt)
	snap.NormalizedAt = optionalTimePtr(normalizedAt)
	snap.ExtractedAt = optionalTimePtr(extractedAt)
	snap.CreatedAt = parseTime(createdAt)
	return snap, nil
}

func optionalTimePtr(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	return &t
}

// GetExtractionState returns the watermark for (session, stage), and whether a
// row existed.
func (s *Store) GetExtractionState(ctx context.Context, sessionID, stage string) (ExtractionState, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT session_id, stage, processed_turn_count, processed_at, status, error
FROM extraction_state WHERE session_id = ? AND stage = ?`, sessionID, stage)
	var st ExtractionState
	var processedAt, errText sql.NullString
	if err := row.Scan(&st.SessionID, &st.Stage, &st.ProcessedTurnCount, &processedAt, &st.Status, &errText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExtractionState{}, false, nil
		}
		return ExtractionState{}, false, fmt.Errorf("get extraction state %q/%q: %w", sessionID, stage, err)
	}
	st.ProcessedAt = optionalTimePtr(processedAt)
	st.Error = errText.String
	return st, true, nil
}

// SetExtractionState upserts the watermark for (session, stage).
func (s *Store) SetExtractionState(ctx context.Context, st ExtractionState) error {
	if strings.TrimSpace(st.Status) == "" {
		st.Status = "ok"
	}
	processedAt := st.ProcessedAt
	if processedAt == nil {
		now := time.Now().UTC()
		processedAt = &now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO extraction_state (session_id, stage, processed_turn_count, processed_at, status, error)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, stage) DO UPDATE SET
  processed_turn_count = excluded.processed_turn_count,
  processed_at = excluded.processed_at,
  status = excluded.status,
  error = excluded.error`,
		st.SessionID, st.Stage, st.ProcessedTurnCount, formatOptionalTime(processedAt),
		st.Status, nullableString(st.Error))
	if err != nil {
		return fmt.Errorf("set extraction state %q/%q: %w", st.SessionID, st.Stage, err)
	}
	return nil
}

// NeedsExtraction reports whether (session, stage) has no watermark yet or a
// watermark below the session's current turn count (i.e. it grew since last run).
func (s *Store) NeedsExtraction(ctx context.Context, sessionID, stage string, currentTurnCount int) (bool, error) {
	st, ok, err := s.GetExtractionState(ctx, sessionID, stage)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return st.ProcessedTurnCount < currentTurnCount, nil
}

// InsertEpisodeMemory inserts a new episodic summary and returns its id.
func (s *Store) InsertEpisodeMemory(ctx context.Context, m EpisodeMemory) (int64, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(m.Outcome) == "" {
		m.Outcome = "unknown"
	}
	if strings.TrimSpace(m.UserFrustration) == "" {
		m.UserFrustration = "unknown"
	}
	domains, err := marshalStringSlice(m.Domains)
	if err != nil {
		return 0, fmt.Errorf("insert episode memory: domains: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO episode_memories (
  session_id, task, outcome, domains_json, worked_steps_json, failed_steps_json,
  user_frustration, candidate_memories_json, evidence_json, model, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.SessionID, m.Task, m.Outcome, domains,
		rawOrEmptyArray(m.WorkedSteps), rawOrEmptyArray(m.FailedSteps),
		m.UserFrustration, rawOrEmptyArray(m.CandidateMemories), rawOrEmptyArray(m.Evidence),
		m.Model, formatTime(m.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("insert episode memory: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert episode memory: last id: %w", err)
	}
	return id, nil
}

// UpsertSemanticMemory finds an existing semantic memory by exact (type, scope,
// statement) match. If found it bumps support_count, appends the evidence
// session id (deduped), refreshes timestamps and recomputes confidence; else it
// inserts a new candidate. Returns the memory id.
func (s *Store) UpsertSemanticMemory(ctx context.Context, m SemanticMemory, evidenceSessionID string) (int64, error) {
	if strings.TrimSpace(m.Scope) == "" {
		m.Scope = "global"
	}
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("upsert semantic memory: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		id           int64
		supportCount int
		contraCount  int
		evidenceJSON string
	)
	row := tx.QueryRowContext(ctx, `
SELECT id, support_count, contradiction_count, evidence_session_ids_json
FROM semantic_memories WHERE type = ? AND scope = ? AND statement = ?`,
		m.Type, m.Scope, m.Statement)
	err = row.Scan(&id, &supportCount, &contraCount, &evidenceJSON)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		evidence := dedupAppend(nil, evidenceSessionID)
		evJSON, mErr := marshalStringSlice(evidence)
		if mErr != nil {
			return 0, fmt.Errorf("upsert semantic memory: evidence: %w", mErr)
		}
		supportCount = 1
		confidence := semanticConfidence(supportCount, 0)
		res, iErr := tx.ExecContext(ctx, `
INSERT INTO semantic_memories (
  type, scope, statement, when_to_apply, support_count, contradiction_count,
  evidence_session_ids_json, confidence, status, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, 0, ?, ?, 'candidate', ?, ?, ?)`,
			m.Type, m.Scope, m.Statement, m.WhenToApply, supportCount,
			evJSON, confidence, formatTime(now), formatTime(now), formatTime(now))
		if iErr != nil {
			return 0, fmt.Errorf("upsert semantic memory: insert: %w", iErr)
		}
		id, iErr = res.LastInsertId()
		if iErr != nil {
			return 0, fmt.Errorf("upsert semantic memory: last id: %w", iErr)
		}
	case err != nil:
		return 0, fmt.Errorf("upsert semantic memory: lookup: %w", err)
	default:
		var evidence []string
		if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
			evidence = nil
		}
		evidence = dedupAppend(evidence, evidenceSessionID)
		evJSON, mErr := marshalStringSlice(evidence)
		if mErr != nil {
			return 0, fmt.Errorf("upsert semantic memory: evidence: %w", mErr)
		}
		supportCount++
		confidence := semanticConfidence(supportCount, contraCount)
		if _, uErr := tx.ExecContext(ctx, `
UPDATE semantic_memories SET
  support_count = ?,
  evidence_session_ids_json = ?,
  confidence = ?,
  last_seen_at = ?,
  updated_at = ?
WHERE id = ?`,
			supportCount, evJSON, confidence, formatTime(now), formatTime(now), id); uErr != nil {
			return 0, fmt.Errorf("upsert semantic memory: update: %w", uErr)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("upsert semantic memory: commit: %w", err)
	}
	return id, nil
}

// semanticConfidence is a simple support/contradiction blend, clamped to 0.95.
func semanticConfidence(support, contradiction int) float64 {
	c := 0.5 + 0.15*float64(support) - 0.2*float64(contradiction)
	if c > 0.95 {
		return 0.95
	}
	if c < 0 {
		return 0
	}
	return c
}

// ListSemanticMemories returns semantic memories; status="" returns all.
func (s *Store) ListSemanticMemories(ctx context.Context, status string) ([]SemanticMemory, error) {
	query := `
SELECT id, type, scope, statement, when_to_apply, support_count, contradiction_count,
       evidence_session_ids_json, confidence, status, first_seen_at, last_seen_at, updated_at
FROM semantic_memories`
	args := []any{}
	if strings.TrimSpace(status) != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list semantic memories: %w", err)
	}
	defer rows.Close()

	memories := []SemanticMemory{}
	for rows.Next() {
		var m SemanticMemory
		var evidenceJSON string
		var firstSeen, lastSeen, updatedAt string
		if err := rows.Scan(&m.ID, &m.Type, &m.Scope, &m.Statement, &m.WhenToApply,
			&m.SupportCount, &m.ContradictionCount, &evidenceJSON, &m.Confidence, &m.Status,
			&firstSeen, &lastSeen, &updatedAt); err != nil {
			return nil, fmt.Errorf("list semantic memories: scan: %w", err)
		}
		m.EvidenceSessionIDs = unmarshalStringSlice(evidenceJSON)
		m.FirstSeenAt = parseTime(firstSeen)
		m.LastSeenAt = parseTime(lastSeen)
		m.UpdatedAt = parseTime(updatedAt)
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// UpsertSkillCandidate inserts a skill candidate or, on target_skill_name
// conflict, updates its body/title/support/confidence. Returns the id.
func (s *Store) UpsertSkillCandidate(ctx context.Context, c SkillCandidate) (int64, error) {
	if strings.TrimSpace(c.Operation) == "" {
		c.Operation = "create"
	}
	if strings.TrimSpace(c.Status) == "" {
		c.Status = "draft"
	}
	if strings.TrimSpace(c.KBStatus) == "" {
		c.KBStatus = "observing"
	}
	if c.SupportCount == 0 {
		c.SupportCount = 1
	}
	now := time.Now().UTC()
	sourceIDs, err := marshalInt64Slice(c.SourceMemoryIDs)
	if err != nil {
		return 0, fmt.Errorf("upsert skill candidate: source ids: %w", err)
	}
	evidence, err := marshalStringSlice(c.EvidenceSessionIDs)
	if err != nil {
		return 0, fmt.Errorf("upsert skill candidate: evidence: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("upsert skill candidate: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO skill_candidates (
  target_skill_name, operation, title, body_md, source_memory_ids_json,
  evidence_session_ids_json, support_count, confidence, status, kb_status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(target_skill_name) DO UPDATE SET
  operation = excluded.operation,
  title = excluded.title,
  body_md = excluded.body_md,
  source_memory_ids_json = excluded.source_memory_ids_json,
  evidence_session_ids_json = excluded.evidence_session_ids_json,
  support_count = excluded.support_count,
  confidence = excluded.confidence,
  updated_at = excluded.updated_at`,
		c.TargetSkillName, c.Operation, c.Title, c.BodyMD, sourceIDs,
		evidence, c.SupportCount, c.Confidence, c.Status, c.KBStatus,
		formatTime(now), formatTime(now)); err != nil {
		return 0, fmt.Errorf("upsert skill candidate: %w", err)
	}

	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM skill_candidates WHERE target_skill_name = ?`, c.TargetSkillName).Scan(&id); err != nil {
		return 0, fmt.Errorf("upsert skill candidate: resolve id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("upsert skill candidate: commit: %w", err)
	}
	return id, nil
}

// ListSkillCandidates returns skill candidates; status="" returns all.
func (s *Store) ListSkillCandidates(ctx context.Context, status string) ([]SkillCandidate, error) {
	query := `
SELECT id, target_skill_name, operation, title, body_md, source_memory_ids_json,
       evidence_session_ids_json, support_count, confidence, status, kb_status,
       created_at, updated_at, published_at
FROM skill_candidates`
	args := []any{}
	if strings.TrimSpace(status) != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list skill candidates: %w", err)
	}
	defer rows.Close()

	candidates := []SkillCandidate{}
	for rows.Next() {
		var c SkillCandidate
		var sourceJSON, evidenceJSON string
		var createdAt, updatedAt string
		var publishedAt sql.NullString
		if err := rows.Scan(&c.ID, &c.TargetSkillName, &c.Operation, &c.Title, &c.BodyMD,
			&sourceJSON, &evidenceJSON, &c.SupportCount, &c.Confidence, &c.Status, &c.KBStatus,
			&createdAt, &updatedAt, &publishedAt); err != nil {
			return nil, fmt.Errorf("list skill candidates: scan: %w", err)
		}
		c.SourceMemoryIDs = unmarshalInt64Slice(sourceJSON)
		c.EvidenceSessionIDs = unmarshalStringSlice(evidenceJSON)
		c.CreatedAt = parseTime(createdAt)
		c.UpdatedAt = parseTime(updatedAt)
		c.PublishedAt = optionalTimePtr(publishedAt)
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// MarkSkillCandidatePublished flips a candidate to published and records its
// kb_status and publish time.
func (s *Store) MarkSkillCandidatePublished(ctx context.Context, id int64, kbStatus string) error {
	if strings.TrimSpace(kbStatus) == "" {
		kbStatus = "in-use"
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE skill_candidates
SET status = 'published', kb_status = ?, published_at = ?, updated_at = ?
WHERE id = ?`, kbStatus, formatTime(now), formatTime(now), id)
	if err != nil {
		return fmt.Errorf("mark skill candidate %d published: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark skill candidate %d published: rows: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("mark skill candidate %d published: not found", id)
	}
	return nil
}

// ---- JSON-blob helpers -----------------------------------------------------

func marshalStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalStringSlice(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return []string{}
	}
	return out
}

func marshalInt64Slice(values []int64) (string, error) {
	if values == nil {
		values = []int64{}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalInt64Slice(value string) []int64 {
	if strings.TrimSpace(value) == "" {
		return []int64{}
	}
	var out []int64
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return []int64{}
	}
	return out
}

// rawOrEmptyArray returns the raw JSON if present, else "[]" for the NOT NULL
// DEFAULT '[]' columns.
func rawOrEmptyArray(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return "[]"
	}
	return string(raw)
}

// dedupAppend appends id to values only if non-empty and not already present.
func dedupAppend(values []string, id string) []string {
	if strings.TrimSpace(id) == "" {
		return values
	}
	for _, v := range values {
		if v == id {
			return values
		}
	}
	return append(values, id)
}
