package memorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Memory-store layer for the agentic context-evolution graph (docs/agents-kb.md). These methods are ADDITIVE to the scalar
// distillation layer in trajectory.go: the agentic nodes (consolidate/verify/
// curate/critic) call them; the original scalar path is untouched so the
// `memory.agentic` flag can flip between the two.

// ---- evolution_runs: one row per cycle -------------------------------------

// EvolutionRun records a single context-evolution cycle for audit + Phase-0
// "what changed" input.
type EvolutionRun struct {
	ID         int64
	StartedAt  time.Time
	FinishedAt *time.Time
	Phase0Plan string // JSON
	CountsJSON string // JSON: {"consolidated":N,"verified":N,"promoted":N,...}
	Digest     string
	Notes      string
}

// StartEvolutionRun inserts a new run row and returns its id.
func (s *Store) StartEvolutionRun(ctx context.Context, plan string) (int64, error) {
	if strings.TrimSpace(plan) == "" {
		plan = "{}"
	}
	now := formatTime(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `
INSERT INTO evolution_runs (started_at, phase0_plan_json, counts_json, digest, notes)
VALUES (?, ?, '{}', '', '')`, now, plan)
	if err != nil {
		return 0, fmt.Errorf("start evolution run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start evolution run: id: %w", err)
	}
	return id, nil
}

// FinishEvolutionRun stamps the run's completion, counts, digest, and notes.
func (s *Store) FinishEvolutionRun(ctx context.Context, id int64, countsJSON, digest, notes string) error {
	if strings.TrimSpace(countsJSON) == "" {
		countsJSON = "{}"
	}
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
UPDATE evolution_runs SET finished_at = ?, counts_json = ?, digest = ?, notes = ? WHERE id = ?`,
		now, countsJSON, digest, notes, id)
	if err != nil {
		return fmt.Errorf("finish evolution run %d: %w", id, err)
	}
	return nil
}

// LastEvolutionRun returns the most recently finished run, for Phase-0 context.
// ok=false when no finished run exists yet.
func (s *Store) LastEvolutionRun(ctx context.Context) (EvolutionRun, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, started_at, finished_at, phase0_plan_json, counts_json, digest, notes
FROM evolution_runs WHERE finished_at IS NOT NULL ORDER BY id DESC LIMIT 1`)
	var (
		r        EvolutionRun
		started  string
		finished sql.NullString
	)
	if err := row.Scan(&r.ID, &started, &finished, &r.Phase0Plan, &r.CountsJSON, &r.Digest, &r.Notes); err != nil {
		if err == sql.ErrNoRows {
			return EvolutionRun{}, false, nil
		}
		return EvolutionRun{}, false, fmt.Errorf("last evolution run: %w", err)
	}
	r.StartedAt = parseStoredTime(started)
	if finished.Valid {
		t := parseStoredTime(finished.String)
		r.FinishedAt = &t
	}
	return r, true, nil
}

// ---- agent_calls: audit + cost/latency ------------------------------------

// AgentCall is one pi-agent invocation record.
type AgentCall struct {
	RunID     int64
	Node      string
	Ref       string // session id or item id the call was about
	PromptSHA string
	Verdict   string
	LatencyMS int64
	OK        bool
	Error     string
}

// RecordAgentCall appends an audit row. Best-effort: callers log-and-continue on
// error (observability must never break the sweep).
func (s *Store) RecordAgentCall(ctx context.Context, c AgentCall) error {
	ok := 0
	if c.OK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_calls (run_id, node, ref, prompt_sha, verdict, latency_ms, ok, error, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.RunID, c.Node, c.Ref, c.PromptSHA, c.Verdict, c.LatencyMS, ok, c.Error,
		formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("record agent call: %w", err)
	}
	return nil
}

// ---- consolidated-memory writes (Node B) -----------------------------------

// ConsolidatedMemory is a merged cluster the Consolidate node produces: one
// canonical statement standing in for many raw candidate members, with support
// derived from the union of the members' evidence sessions.
type ConsolidatedMemory struct {
	Type               string
	Scope              string
	Statement          string
	WhenToApply        string
	EvidenceSessionIDs []string // union across cluster members (DISTINCT)
	MemberIDs          []int64  // raw candidate ids this canonical replaces
	AgentRationale     string
	Confidence         float64
}

// ReplaceWithConsolidated deletes the given raw candidate rows and inserts the
// canonical merged memory in one transaction. support_count is computed here as
// the count of DISTINCT evidence sessions — the agent decided membership, code
// counts support. status starts at 'candidate'; the curator (Node D) promotes.
func (s *Store) ReplaceWithConsolidated(ctx context.Context, cm ConsolidatedMemory) (int64, error) {
	now := formatTime(time.Now().UTC())
	evidence := cm.EvidenceSessionIDs
	if evidence == nil {
		evidence = []string{}
	}
	evJSON, err := marshalStringSlice(evidence)
	if err != nil {
		return 0, fmt.Errorf("consolidated: evidence: %w", err)
	}
	membersJSON, err := marshalInt64Slice(cm.MemberIDs)
	if err != nil {
		return 0, fmt.Errorf("consolidated: members: %w", err)
	}
	support := len(evidence)
	if support < 1 {
		support = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("consolidated: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range cm.MemberIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM semantic_memories WHERE id = ?`, id); err != nil {
			return 0, fmt.Errorf("consolidated: delete member %d: %w", id, err)
		}
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO semantic_memories (
  type, scope, statement, when_to_apply, support_count, contradiction_count,
  evidence_session_ids_json, confidence, status, canonical_of_json, agent_rationale,
  first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, 0, ?, ?, 'candidate', ?, ?, ?, ?, ?)`,
		normalizeSemType(cm.Type), defaultScope(cm.Scope), cm.Statement, cm.WhenToApply,
		support, evJSON, cm.Confidence, membersJSON, cm.AgentRationale, now, now, now)
	if err != nil {
		return 0, fmt.Errorf("consolidated: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("consolidated: id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("consolidated: commit: %w", err)
	}
	return id, nil
}

// ---- verify + curate updates (Nodes C, D) ----------------------------------

// SetMemoryVerdict records Node C's verification result on a memory. A stale/
// fixed verdict bumps contradiction_count so the curator excludes it. A non-empty
// corrected replaces the statement (e.g. a fixed path).
func (s *Store) SetMemoryVerdict(ctx context.Context, id int64, verdict, corrected string, bumpContradiction bool) error {
	now := formatTime(time.Now().UTC())
	q := `UPDATE semantic_memories SET verify_verdict = ?, verify_checked_at = ?, updated_at = ?`
	args := []any{verdict, now, now}
	if strings.TrimSpace(corrected) != "" {
		q += `, corrected_statement = ?, statement = ?`
		args = append(args, corrected, corrected)
	}
	if bumpContradiction {
		q += `, contradiction_count = contradiction_count + 1`
	}
	q += ` WHERE id = ?`
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("set memory verdict %d: %w", id, err)
	}
	return nil
}

// SetMemoryStatus sets a memory's lifecycle status (candidate|accepted|
// deprecated), used by the curator (Node D) and the consistency critic (Node F,
// which can send a promoted item back to candidate via a veto).
func (s *Store) SetMemoryStatus(ctx context.Context, id int64, status string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE semantic_memories SET status = ?, updated_at = ? WHERE id = ?`,
		status, formatTime(time.Now().UTC()), id); err != nil {
		return fmt.Errorf("set memory status %d: %w", id, err)
	}
	return nil
}

// SetMemoryCuration stores the curator's rationale alongside a status change.
func (s *Store) SetMemoryCuration(ctx context.Context, id int64, status, rationale string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE semantic_memories SET status = ?, agent_rationale = ?, updated_at = ? WHERE id = ?`,
		status, rationale, formatTime(time.Now().UTC()), id); err != nil {
		return fmt.Errorf("set memory curation %d: %w", id, err)
	}
	return nil
}

// ---- small helpers (kept local to the evolution layer) ---------------------

func defaultScope(s string) string {
	if strings.TrimSpace(s) == "" {
		return "global"
	}
	return strings.TrimSpace(s)
}

func normalizeSemType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fact", "heuristic", "procedure", "bug", "preference":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "fact"
	}
}

// parseStoredTime parses a stored RFC3339-ish timestamp, tolerating the store's
// formatTime output. Returns zero time on parse failure.
func parseStoredTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// mustJSON marshals v to a compact JSON string, returning "{}" on error so
// callers can use it inline for audit/report fields.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
