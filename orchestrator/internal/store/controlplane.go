package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

var errDuplicateOutputSequenceMismatch = errors.New("duplicate output_sequence mismatch")

// errPermanentTurnCompletion marks a CompleteTurn error as a definitive
// rejection: a CAS conflict (the turn/generation lease no longer holds) or a
// driver-state validation failure (the bridge supplied semantically invalid
// data). Such errors can never succeed on retry, so the caller may retire the
// generation. Transient/infra errors (e.g. "database is locked", BeginTx /
// Commit failures, the driver-state lease-check query error) are deliberately
// left unmarked so the caller retries them instead of failing the generation.
var errPermanentTurnCompletion = errors.New("permanent turn completion failure")

// IsPermanentTurnCompletion reports whether a CompleteTurn error is a definitive
// rejection that must not be retried.
func IsPermanentTurnCompletion(err error) bool {
	return errors.Is(err, errPermanentTurnCompletion)
}

// permanentTurnCompletionf builds a new permanent turn-completion error.
func permanentTurnCompletionf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errPermanentTurnCompletion}, args...)...)
}

// permanentTurnCompletion marks an existing error as a permanent
// turn-completion rejection while preserving it for errors.Is/As inspection.
func permanentTurnCompletion(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errPermanentTurnCompletion, err)
}

// permanentRequireOneRow behaves like requireOneRow but classifies the
// "affected != 1" CAS conflict as a permanent turn-completion failure. A
// RowsAffected() transport error is left unmarked so it is retried.
func permanentRequireOneRow(result sql.Result, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return permanentTurnCompletionf(message)
	}
	return nil
}

type ClaimNextTurnParams struct {
	SessionID    string
	GenerationID string
	Owner        string
	RequestID    string
	LeaseTTL     time.Duration
	Now          time.Time
}

type BridgeProtocolEvidence struct {
	DriverID              string
	ProtocolVersion       int
	TurnInputSchema       string
	AgentManifestDigest   string
	RuntimeConfigDigest   string
	ContractGateVersion   string
	SandboxContractDigest string
}

type ResumeTurnParams struct {
	SessionID       string
	GenerationID    string
	TurnID          int64
	Owner           string
	LeaseTTL        time.Duration
	AckStartedGrace time.Duration
	Now             time.Time
}

type TurnGrant struct {
	TurnID             int64
	Sequence           int64
	Content            string
	Attempt            int
	Replayed           bool
	ExpiresAt          time.Time
	DriverState        DriverStateToken
	DriverStatePayload []byte
}

type BridgeHelloAck struct {
	LastOutputSequenceByTurn map[int64]int64
	LeasedTurnID             *int64
	ServerTime               time.Time
}

type AppendEventParams struct {
	SessionID      string
	TurnID         *int64
	GenerationID   string
	Owner          string
	OutputSequence *int64
	DedupeKey      string
	ProxyRequestID string
	Stream         string
	Severity       string
	Type           string
	Payload        any
	Now            time.Time
}

type AckStartedParams struct {
	SessionID       string
	GenerationID    string
	TurnID          int64
	Owner           string
	SandboxSourceIP string
	LeaseTTL        time.Duration
	EventType       string
	EventDedupeKey  string
	EventPayload    any
	Now             time.Time
}

type CompleteTurnParams struct {
	FinalOutputSequence *int64
	SessionID           string
	GenerationID        string
	TurnID              int64
	Owner               string
	TerminalStatus      string
	ErrorClass          string
	Error               string
	DriverStateUpdate   *DriverStateUpdate
	EventType           string
	EventDedupeKey      string
	EventPayload        any
	Now                 time.Time
}

type RenewHeartbeatParams struct {
	SessionID    string
	GenerationID string
	Owner        string
	LeaseTTL     time.Duration
	Now          time.Time
}

type RenewGenerationStartLeaseParams struct {
	SessionID    string
	GenerationID string
	Owner        string
	LeaseTTL     time.Duration
	Now          time.Time
}

type FailGenerationParams struct {
	SessionID    string
	GenerationID string
	TurnID       int64
	Owner        string
	ErrorClass   string
	Reason       string
	Now          time.Time
}

// FailGenerationResult is the durable outcome of retiring a generation after
// a bridge/runtime failure. EventID lets the server publish the exact committed
// event to live subscribers while preserving replay from the events table.
type FailGenerationResult struct {
	EventID       int64
	SessionStatus string
}

type FailGenerationStartParams struct {
	SessionID      string
	GenerationID   string
	Owner          string
	SessionStatus  string
	ErrorClass     string
	Reason         string
	EventType      string
	EventDedupeKey string
	Now            time.Time
}

func (s *Store) EnqueueTurn(ctx context.Context, sessionID, content string, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	next, err := nextTurnSequence(ctx, tx, sessionID)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO turns (session_id, sequence, role, content, status, attempt, created_at)
VALUES (?, ?, 'user', ?, 'queued', 0, ?)`, sessionID, next, content, formatTime(now))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// SessionHasActiveTurn reports whether the session has any turn that is queued,
// leased, or running. Callers use it to reject workspace mutations (uploads,
// online edits) while the agent may be actively reading or writing the
// workspace, mirroring the pending-turn check in generation idle CAS.
// InflightTurnForGeneration returns the turn currently leased to or running on
// a generation, if any. At most one exists: the claim CAS refuses a second
// in-flight turn per generation. ok=false means no in-flight turn.
func (s *Store) InflightTurnForGeneration(ctx context.Context, sessionID, generationID string) (turnID int64, status string, ok bool, err error) {
	err = s.reader().QueryRowContext(ctx, `
SELECT id, status
FROM turns
WHERE session_id = ?
  AND generation_id = ?
  AND status IN ('leased', 'running')
ORDER BY id DESC
LIMIT 1`, sessionID, generationID).Scan(&turnID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return turnID, status, true, nil
}

func (s *Store) SessionHasActiveTurn(ctx context.Context, sessionID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND `+pendingTurnPredicate+``, sessionID).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *Store) BridgeHelloAck(ctx context.Context, sessionID, generationID, owner string, now time.Time, ackStartedGrace time.Duration) (BridgeHelloAck, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BridgeHelloAck{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	activeLeaseQuery := `
SELECT COUNT(*)
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND g.lease_owner = ?
  AND g.status IN ('idle','active','probing','restoring','starting')
  AND g.lease_expires_at > ?
  AND s.active_generation_id = ?`
	if err := tx.QueryRowContext(ctx, activeLeaseQuery, sessionID, generationID, owner, formatTime(now), generationID).Scan(&exists); err != nil {
		return BridgeHelloAck{}, err
	}
	if exists != 1 {
		if ackStartedGrace <= 0 {
			return BridgeHelloAck{}, fmt.Errorf("bridge hello generation CAS failed")
		}
		cutoff := now.Add(-ackStartedGrace)
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND g.status IN ('active', 'idle')
  AND g.lease_expires_at IS NOT NULL
  AND g.lease_expires_at <= ?
  AND g.lease_expires_at > ?
  AND s.active_generation_id = ?
  AND s.status NOT IN ('failed', 'destroyed')
  AND EXISTS (
    SELECT 1 FROM turns t
    WHERE t.session_id = g.session_id
      AND t.generation_id = g.generation_id
      AND t.status = 'running'
      AND t.ack_started_at IS NOT NULL
      AND t.lease_expires_at IS NOT NULL
      AND t.lease_expires_at <= ?
      AND t.lease_expires_at > ?
  )`, sessionID, generationID, formatTime(now), formatTime(cutoff), generationID, formatTime(now), formatTime(cutoff)).Scan(&exists); err != nil {
			return BridgeHelloAck{}, err
		}
		if exists != 1 {
			return BridgeHelloAck{}, fmt.Errorf("bridge hello generation CAS failed")
		}
	}
	ack := BridgeHelloAck{
		LastOutputSequenceByTurn: map[int64]int64{},
		ServerTime:               now,
	}
	rows, err := tx.QueryContext(ctx, `
SELECT t.id, COALESCE(MAX(e.output_sequence), 0)
FROM turns t
LEFT JOIN (
  SELECT turn_id,generation_id,output_sequence FROM bridge_ingress
  UNION ALL SELECT turn_id,generation_id,output_sequence FROM events
) e ON e.turn_id = t.id
  AND e.generation_id = t.generation_id
  AND e.output_sequence IS NOT NULL
WHERE t.session_id = ?
  AND t.generation_id = ?
  AND t.status IN ('leased','running')
GROUP BY t.id
ORDER BY t.id`, sessionID, generationID)
	if err != nil {
		return BridgeHelloAck{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var turnID int64
		var lastSequence int64
		if err := rows.Scan(&turnID, &lastSequence); err != nil {
			return BridgeHelloAck{}, err
		}
		ack.LastOutputSequenceByTurn[turnID] = lastSequence
		if ack.LeasedTurnID == nil {
			id := turnID
			ack.LeasedTurnID = &id
		}
	}
	if err := rows.Err(); err != nil {
		return BridgeHelloAck{}, err
	}
	return ack, tx.Commit()
}

func (s *Store) BridgeProtocolEvidence(ctx context.Context, sessionID, generationID string) (BridgeProtocolEvidence, error) {
	record, err := getSandboxContractForGenerationWithMirrors(ctx, s.db, sessionID, generationID)
	if err != nil {
		return BridgeProtocolEvidence{}, err
	}
	if record.ContractGateVersion != SandboxContractGateDriverManifest {
		return BridgeProtocolEvidence{}, fmt.Errorf("bridge protocol v2 requires driver manifest evidence, got %s", record.ContractGateVersion)
	}
	object, err := decodeSandboxContractObject(record.CanonicalPayload)
	if err != nil {
		return BridgeProtocolEvidence{}, err
	}
	driver, ok := object["driver"].(map[string]any)
	if !ok {
		return BridgeProtocolEvidence{}, fmt.Errorf("sandbox contract missing driver object")
	}
	driverID := strings.TrimSpace(stringValue(driver["driver_id"]))
	if driverID == "" {
		return BridgeProtocolEvidence{}, fmt.Errorf("sandbox contract missing driver_id")
	}
	protocolVersion := 0
	switch value := driver["bridge_protocol_version"].(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			protocolVersion = int(parsed)
		}
	case float64:
		protocolVersion = int(value)
	}
	turnInputSchema := strings.TrimSpace(stringValue(driver["turn_input_schema"]))
	inputDigests, ok := object["input_digests"].(map[string]any)
	if !ok {
		return BridgeProtocolEvidence{}, fmt.Errorf("sandbox contract missing input_digests")
	}
	agentManifestDigest := strings.TrimSpace(stringValue(inputDigests["agent_manifest_digest"]))
	runtimeConfigDigest := strings.TrimSpace(stringValue(inputDigests["runtime_config_digest"]))
	if !strings.HasPrefix(agentManifestDigest, "sha256:") || !strings.HasPrefix(runtimeConfigDigest, "sha256:") {
		return BridgeProtocolEvidence{}, fmt.Errorf("bridge protocol v2 requires manifest-backed input digests")
	}
	return BridgeProtocolEvidence{
		DriverID:              driverID,
		ProtocolVersion:       protocolVersion,
		TurnInputSchema:       turnInputSchema,
		AgentManifestDigest:   agentManifestDigest,
		RuntimeConfigDigest:   runtimeConfigDigest,
		ContractGateVersion:   record.ContractGateVersion,
		SandboxContractDigest: record.SandboxContractDigest,
	}, nil
}

func (s *Store) ClaimNextTurn(ctx context.Context, p ClaimNextTurnParams) (TurnGrant, bool, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnGrant{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	replay, ok, err := claimReplay(ctx, tx, p)
	if err != nil {
		return TurnGrant{}, false, err
	}
	if ok {
		replay.Replayed = true
		return replay, true, tx.Commit()
	}

	// auto_dispatch = 1 is what makes parking real: a turn the user parked with Stop
	// stays queued and visible but is never a claim candidate, so the worker cannot
	// start it on its own. Releasing one flips this back (ReleaseParkedTurn), and
	// because only the released turn is eligible, `sequence ASC` then selects it
	// without needing any notion of priority.
	var grant TurnGrant
	err = tx.QueryRowContext(ctx, `
SELECT id, sequence, content, attempt
FROM turns
WHERE session_id = ?
  AND status = 'queued'
  AND auto_dispatch = 1
  AND lease_owner IS NULL
ORDER BY sequence ASC
LIMIT 1`, p.SessionID).Scan(&grant.TurnID, &grant.Sequence, &grant.Content, &grant.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnGrant{}, false, tx.Commit()
	}
	if err != nil {
		return TurnGrant{}, false, err
	}
	expiresAt := p.Now.Add(p.LeaseTTL)

	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'leased',
    generation_id = ?,
    lease_owner = ?,
    lease_expires_at = ?,
    claim_request_id = ?,
    claim_granted_at = ?
WHERE id = ?
  AND session_id = ?
  AND status = 'queued'
  AND auto_dispatch = 1
  AND lease_owner IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE generation_id = ?
      AND status IN ('leased', 'running')
  )
  AND EXISTS (
    SELECT 1 FROM runtime_generations g
    JOIN sessions s ON s.id = g.session_id
    JOIN runtime_resource_instances ri ON ri.generation_id = g.generation_id
    WHERE g.generation_id = ?
      AND g.session_id = ?
      AND g.sandbox_contract_version = 'sandbox-isolation-v1'
      AND g.status IN ('idle', 'active')
      AND g.lease_owner = ?
      AND g.lease_expires_at > ?
      AND s.active_generation_id = ?
      AND ri.state = 'live'
      AND ri.sandbox_contract_version = 'sandbox-isolation-v1'
      AND ri.contract_id = g.sandbox_contract_id
	)`,
		p.GenerationID, p.Owner, formatTime(expiresAt), p.RequestID, formatTime(p.Now), grant.TurnID,
		p.SessionID, p.GenerationID, p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.GenerationID)
	if err != nil {
		return TurnGrant{}, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return TurnGrant{}, false, err
	}
	if affected != 1 {
		return TurnGrant{}, false, tx.Commit()
	}
	if _, err := getSandboxContractForGenerationWithMirrors(ctx, tx, p.SessionID, p.GenerationID); err != nil {
		return TurnGrant{}, false, err
	}

	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'active'
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('idle', 'active')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
  )`, p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return TurnGrant{}, false, err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return TurnGrant{}, false, err
	}
	if affected != 1 {
		return TurnGrant{}, false, fmt.Errorf("generation CAS failed after turn claim")
	}

	// Register the sandbox's model-request context now, not on ack. The agent
	// receives this grant and issues its first model call over HTTP, while its ack
	// travels back through the bridge file queue — so the call routinely arrives
	// first, and before this it found no context and was refused. See
	// upsertActiveModelRequestContextTx.
	sandboxSourceIP, err := sandboxSourceIPForGenerationTx(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return TurnGrant{}, false, err
	}
	var modelAccessAllowed int
	if err := tx.QueryRowContext(ctx, `
SELECT a.model_access_allowed
FROM runtime_generations g
JOIN agent_runtime_profiles a ON a.agent_runtime_profile_id = g.agent_runtime_profile_id
WHERE g.session_id = ?
  AND g.generation_id = ?`, p.SessionID, p.GenerationID).Scan(&modelAccessAllowed); err != nil {
		return TurnGrant{}, false, err
	}
	if err := upsertActiveModelRequestContextTx(ctx, tx, activeModelRequestContextParams{
		SandboxSourceIP:    sandboxSourceIP,
		SessionID:          p.SessionID,
		GenerationID:       p.GenerationID,
		TurnID:             grant.TurnID,
		Owner:              p.Owner,
		ExpiresAt:          expiresAt,
		ModelAccessAllowed: modelAccessAllowed,
		Now:                p.Now,
	}); err != nil {
		return TurnGrant{}, false, err
	}

	grant, err = turnGrantByID(ctx, tx, p.SessionID, p.GenerationID, grant.TurnID, p.Owner, p.Now)
	if err != nil {
		return TurnGrant{}, false, err
	}
	grant.ExpiresAt = expiresAt
	return grant, true, tx.Commit()
}

func (s *Store) ResumeTurn(ctx context.Context, p ResumeTurnParams) (TurnGrant, bool, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnGrant{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	expiresAt := p.Now.Add(p.LeaseTTL)
	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET lease_expires_at = ?
WHERE id = ?
  AND session_id = ?
  AND generation_id = ?
  AND status IN ('leased', 'running')
  AND lease_owner = ?
  AND EXISTS (
    SELECT 1 FROM runtime_generations g
    JOIN sessions s ON s.id = g.session_id
    WHERE g.generation_id = ?
      AND g.session_id = ?
      AND g.status IN ('active', 'idle')
      AND g.lease_owner = ?
      AND g.lease_expires_at > ?
      AND s.active_generation_id = ?
  )`, formatTime(expiresAt), p.TurnID, p.SessionID, p.GenerationID, p.Owner,
		p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.GenerationID)
	if err != nil {
		return TurnGrant{}, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return TurnGrant{}, false, err
	}
	recoveringExpired := false
	if affected != 1 {
		if p.AckStartedGrace <= 0 {
			return TurnGrant{}, false, tx.Commit()
		}
		if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
			return TurnGrant{}, false, err
		}
		cutoff := p.Now.Add(-p.AckStartedGrace)
		res, err = tx.ExecContext(ctx, `
UPDATE turns
SET lease_owner = ?,
    lease_expires_at = ?
WHERE id = ?
  AND session_id = ?
  AND generation_id = ?
  AND status = 'running'
  AND ack_started_at IS NOT NULL
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM runtime_generations g
    JOIN sessions s ON s.id = g.session_id
    WHERE g.generation_id = ?
      AND g.session_id = ?
      AND g.status IN ('active', 'idle')
      AND g.lease_expires_at IS NOT NULL
      AND g.lease_expires_at <= ?
      AND g.lease_expires_at > ?
      AND s.active_generation_id = ?
      AND s.status NOT IN ('failed', 'destroyed')
  )`, p.Owner, formatTime(expiresAt), p.TurnID, p.SessionID, p.GenerationID,
			formatTime(p.Now), formatTime(cutoff), p.GenerationID, p.SessionID,
			formatTime(p.Now), formatTime(cutoff), p.GenerationID)
		if err != nil {
			return TurnGrant{}, false, err
		}
		affected, err = res.RowsAffected()
		if err != nil {
			return TurnGrant{}, false, err
		}
		if affected != 1 {
			return TurnGrant{}, false, tx.Commit()
		}
		recoveringExpired = true
	}
	var generationUpdate string
	var generationArgs []any
	if recoveringExpired {
		cutoff := p.Now.Add(-p.AckStartedGrace)
		generationUpdate = `
UPDATE runtime_generations
SET status = 'active',
    lease_owner = ?,
    lease_expires_at = ?,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('active', 'idle')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status NOT IN ('failed', 'destroyed')
  )`
		generationArgs = []any{
			p.Owner, formatTime(expiresAt), formatTime(p.Now), p.GenerationID, p.SessionID,
			formatTime(p.Now), formatTime(cutoff), p.SessionID, p.GenerationID,
		}
	} else {
		generationUpdate = `
UPDATE runtime_generations
SET status = 'active',
    lease_expires_at = ?,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('active', 'idle')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
  )`
		generationArgs = []any{
			formatTime(expiresAt), formatTime(p.Now), p.GenerationID, p.SessionID,
			p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID,
		}
	}
	res, err = tx.ExecContext(ctx, generationUpdate, generationArgs...)
	if err != nil {
		return TurnGrant{}, false, err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return TurnGrant{}, false, err
	}
	if affected != 1 {
		return TurnGrant{}, false, fmt.Errorf("generation CAS failed after turn resume")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE active_model_request_contexts
SET expires_at = ?,
    lease_owner = ?,
    updated_at = ?
WHERE session_id = ?
  AND generation_id = ?
  AND turn_id = ?
  AND turn_id IN (
    SELECT id FROM turns
    WHERE session_id = ?
      AND generation_id = ?
      AND lease_owner = ?
      AND status = 'running'
  )`, formatTime(expiresAt), p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID, p.TurnID, p.SessionID, p.GenerationID, p.Owner); err != nil {
		return TurnGrant{}, false, err
	}
	grant, err := turnGrantByID(ctx, tx, p.SessionID, p.GenerationID, p.TurnID, p.Owner, p.Now)
	if err != nil {
		return TurnGrant{}, false, err
	}
	grant.ExpiresAt = expiresAt
	return grant, true, tx.Commit()
}

// activeModelRequestContextParams registers a sandbox's right to make model
// requests for one turn.
type activeModelRequestContextParams struct {
	SandboxSourceIP    string
	SessionID          string
	GenerationID       string
	TurnID             int64
	Owner              string
	ExpiresAt          time.Time
	ModelAccessAllowed int
	Now                time.Time
}

// upsertActiveModelRequestContextTx registers (or refreshes) the proxy context
// for a turn. Called from BOTH ClaimNextTurn and AckTurnStarted.
//
// Registering at claim is the load-bearing part. The context used to be created
// only on ack, but ack travels back through the bridge file queue and is applied
// on the orchestrator's next poll, while the agent's first model request goes
// straight to the proxy over HTTP. On a warm sandbox the agent wins that race
// essentially always, so its opening model call for the turn found no context and
// was denied — the turn then died reporting a model failure, while the prompt
// itself stayed in the agent's history (which is why the agent "remembered"
// messages it never answered). A cold first turn masked this because sandbox and
// agent startup gave the ack time to land.
//
// The claim is the correct moment regardless: that is when the turn becomes this
// sandbox's to execute, and the lease bounds it.
//
// next_request_sequence is PRESERVED when the row already refers to the same
// turn. The ack arrives after the agent has usually issued requests, and resetting
// the counter there would restart sequences mid-turn and collide with the
// proxy.request.started events already recorded for it.
func upsertActiveModelRequestContextTx(ctx context.Context, tx *sql.Tx, p activeModelRequestContextParams) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO active_model_request_contexts (
  sandbox_source_ip, session_id, generation_id, turn_id,
  lease_owner, expires_at, model_access_allowed, next_request_sequence, registered_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT(sandbox_source_ip) DO UPDATE SET
  session_id = excluded.session_id,
  generation_id = excluded.generation_id,
  turn_id = excluded.turn_id,
  lease_owner = excluded.lease_owner,
  expires_at = excluded.expires_at,
  model_access_allowed = excluded.model_access_allowed,
  next_request_sequence = CASE
    WHEN active_model_request_contexts.turn_id = excluded.turn_id
      AND active_model_request_contexts.generation_id = excluded.generation_id
    THEN active_model_request_contexts.next_request_sequence
    ELSE excluded.next_request_sequence
  END,
  registered_at = CASE
    WHEN active_model_request_contexts.turn_id = excluded.turn_id
      AND active_model_request_contexts.generation_id = excluded.generation_id
    THEN active_model_request_contexts.registered_at
    ELSE excluded.registered_at
  END,
  updated_at = excluded.updated_at`,
		p.SandboxSourceIP, p.SessionID, p.GenerationID, p.TurnID, p.Owner,
		formatTime(p.ExpiresAt), p.ModelAccessAllowed, formatTime(p.Now), formatTime(p.Now))
	return err
}

func (s *Store) AckTurnStarted(ctx context.Context, p AckStartedParams) (int64, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := getSandboxContractForGenerationWithMirrors(ctx, tx, p.SessionID, p.GenerationID); err != nil {
		return 0, err
	}
	expiresAt := p.Now.Add(p.LeaseTTL)
	var alreadyRunning int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE id = ?
  AND status = 'running'
  AND ack_started_at IS NOT NULL
  AND session_id = ?
  AND generation_id = ?
  AND lease_owner = ?
  AND lease_expires_at > ?`,
		p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now)).Scan(&alreadyRunning); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'running',
    started_at = ?,
    ack_started_at = ?
WHERE id = ?
  AND status = 'leased'
  AND session_id = ?
  AND generation_id = ?
  AND lease_owner = ?
  AND lease_expires_at > ?`,
		formatTime(p.Now), formatTime(p.Now), p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now))
	if err != nil {
		return 0, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if affected != 1 && alreadyRunning != 1 {
		return 0, fmt.Errorf("turn ack_started CAS failed")
	}
	startedNow := alreadyRunning != 1
	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generations
SET last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND sandbox_contract_version = 'sandbox-isolation-v1'
  AND status = 'active'
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
  )
  AND EXISTS (
    SELECT 1 FROM runtime_resource_instances ri
    WHERE ri.generation_id = ?
      AND ri.state = 'live'
      AND ri.sandbox_contract_version = 'sandbox-isolation-v1'
      AND ri.contract_id = runtime_generations.sandbox_contract_id
  )`, formatTime(p.Now), p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID, p.GenerationID)
	if err != nil {
		return 0, err
	}
	if err := requireOneRow(res, "generation ack_started CAS failed"); err != nil {
		return 0, err
	}
	sandboxSourceIP, err := sandboxSourceIPForGenerationTx(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return 0, err
	}
	var modelAccessAllowed int
	if err := tx.QueryRowContext(ctx, `
SELECT a.model_access_allowed
FROM runtime_generations g
JOIN agent_runtime_profiles a ON a.agent_runtime_profile_id = g.agent_runtime_profile_id
WHERE g.session_id = ?
  AND g.generation_id = ?`, p.SessionID, p.GenerationID).Scan(&modelAccessAllowed); err != nil {
		return 0, err
	}
	if startedNow {
		if err := upsertActiveModelRequestContextTx(ctx, tx, activeModelRequestContextParams{
			SandboxSourceIP:    sandboxSourceIP,
			SessionID:          p.SessionID,
			GenerationID:       p.GenerationID,
			TurnID:             p.TurnID,
			Owner:              p.Owner,
			ExpiresAt:          expiresAt,
			ModelAccessAllowed: modelAccessAllowed,
			Now:                p.Now,
		}); err != nil {
			return 0, err
		}
	}
	// The agent has the turn and is about to build its first model request for it,
	// so this is the moment the user's prompt becomes a transcript entry. Writing
	// it here rather than at enqueue makes `created_at` the time the prompt actually
	// reached the model, and means a queued turn has no message row to leak into
	// the transcript. Inside this transaction, so the row and the running status
	// commit together. Idempotent for a replayed ack.
	if startedNow {
		if _, _, err := materializeTurnMessageTx(ctx, tx, p.SessionID, p.TurnID, p.Now); err != nil {
			return 0, err
		}
	}
	var eventID int64
	if p.EventType != "" {
		eventID, err = appendEventTx(ctx, tx, AppendEventParams{
			SessionID:    p.SessionID,
			TurnID:       &p.TurnID,
			GenerationID: p.GenerationID,
			DedupeKey:    p.EventDedupeKey,
			Type:         p.EventType,
			Payload:      p.EventPayload,
			Now:          p.Now,
		})
		if err != nil {
			return 0, err
		}
	}
	return eventID, tx.Commit()
}

func sandboxSourceIPForGenerationTx(ctx context.Context, tx *sql.Tx, sessionID, generationID string) (string, error) {
	var sandboxIP string
	if err := tx.QueryRowContext(ctx, `
SELECT ri.sandbox_ip
FROM runtime_resource_instances ri
JOIN runtime_generations g ON g.generation_id = ri.generation_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND g.sandbox_contract_version = 'sandbox-isolation-v1'
  AND ri.state = 'live'
  AND ri.sandbox_contract_version = 'sandbox-isolation-v1'
  AND ri.contract_id = g.sandbox_contract_id`, sessionID, generationID).Scan(&sandboxIP); err != nil {
		return "", err
	}
	addr, err := netip.ParseAddr(sandboxIP)
	if err != nil {
		return "", fmt.Errorf("invalid runtime resource sandbox_ip %q: %w", sandboxIP, err)
	}
	return addr.String(), nil
}

func (s *Store) CompleteTurn(ctx context.Context, p CompleteTurnParams) (int64, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	switch p.TerminalStatus {
	case "completed", "failed", "canceled":
	default:
		return 0, permanentTurnCompletionf("invalid terminal turn status %q", p.TerminalStatus)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bridge_ingress i
      LEFT JOIN bridge_projections bp ON bp.generation_id=i.generation_id AND bp.turn_id=i.turn_id AND bp.attempt=i.attempt
      WHERE i.session_id=? AND i.generation_id=? AND i.turn_id=? AND i.id>coalesce(bp.last_record_id,0)
      AND EXISTS(SELECT 1 FROM bridge_ingress c WHERE c.generation_id=i.generation_id AND c.turn_id=i.turn_id AND c.attempt=i.attempt AND c.record_key='completion')
      AND EXISTS(SELECT 1 FROM turns t WHERE t.id=i.turn_id AND t.status IN ('leased','running')))`, p.SessionID, p.GenerationID, p.TurnID).Scan(&pending); err != nil {
		return 0, err
	}
	if pending != 0 {
		return 0, fmt.Errorf("bridge completion awaits durable projection")
	}
	if err := assertCompletionOutputTx(ctx, tx, p); err != nil {
		return 0, err
	}
	if err := applyDriverStateUpdateTx(ctx, tx, p); err != nil {
		return 0, err
	}
	var alreadyTerminal int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE id = ?
  AND status = ?
  AND session_id = ?
  AND generation_id = ?
  AND completed_by_generation = ?`,
		p.TurnID, p.TerminalStatus, p.SessionID, p.GenerationID, p.GenerationID).Scan(&alreadyTerminal); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = ?,
    completed_at = ?,
    completed_by_generation = ?,
    error_class = ?,
    error = ?
WHERE id = ?
  AND status IN ('leased', 'running')
  AND session_id = ?
  AND generation_id = ?
  AND lease_owner = ?
  AND lease_expires_at > ?`,
		p.TerminalStatus, formatTime(p.Now), p.GenerationID, nullableString(p.ErrorClass), nullableString(p.Error),
		p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now))
	if err != nil {
		return 0, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if affected != 1 && alreadyTerminal != 1 {
		return 0, permanentTurnCompletionf("turn completion CAS failed")
	}
	sessionMarkedIdle := false
	if alreadyTerminal != 1 {
		sessionMarkedIdle, err = markGenerationAndSessionIdleIfNoInflight(ctx, tx, p.SessionID, p.GenerationID, p.TurnID, p.Owner, p.Now)
		if err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM active_model_request_contexts
WHERE session_id = ?
  AND generation_id = ?
  AND turn_id = ?`, p.SessionID, p.GenerationID, p.TurnID); err != nil {
		return 0, err
	}
	var eventID int64
	if p.EventType != "" {
		eventPayload, err := completeTurnEventPayload(p.EventPayload, p, sessionMarkedIdle)
		if err != nil {
			return 0, err
		}
		eventID, err = appendEventTx(ctx, tx, AppendEventParams{
			SessionID:    p.SessionID,
			TurnID:       &p.TurnID,
			GenerationID: p.GenerationID,
			DedupeKey:    p.EventDedupeKey,
			Type:         p.EventType,
			Payload:      eventPayload,
			Now:          p.Now,
		})
		if err != nil {
			return 0, err
		}
	}
	return eventID, tx.Commit()
}

func completeTurnEventPayload(base any, p CompleteTurnParams, sessionMarkedIdle bool) (map[string]any, error) {
	payload := map[string]any{}
	if base != nil {
		raw, err := json.Marshal(base)
		if err != nil {
			return nil, err
		}
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, err
			}
		}
	}
	delete(payload, "driver_state_update")
	payload["session_marked_idle"] = sessionMarkedIdle
	payload["session_terminal"] = false
	if sessionMarkedIdle {
		payload["session_status"] = "running_idle"
		payload["session_updated_at"] = formatTime(p.Now)
		payload["session_last_activity_at"] = formatTime(p.Now)
		payload["active_generation_id"] = p.GenerationID
	}
	return payload, nil
}

func (s *Store) RenewGenerationHeartbeat(ctx context.Context, p RenewHeartbeatParams) error {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	expiresAt := p.Now.Add(p.LeaseTTL)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET lease_expires_at = ?,
    last_seen_at = ?,
    sandbox_last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('starting','probing','active','idle','checkpointing','restoring')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
  )`, formatTime(expiresAt), formatTime(p.Now), formatTime(p.Now), p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation heartbeat CAS failed"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE turns
SET lease_expires_at = ?
WHERE session_id = ?
  AND generation_id = ?
  AND status IN ('leased', 'running')
  AND lease_owner = ?
  AND lease_expires_at > ?`, formatTime(expiresAt), p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE active_model_request_contexts
SET expires_at = ?,
    lease_owner = ?,
    updated_at = ?
WHERE session_id = ?
  AND generation_id = ?
  AND turn_id IN (
    SELECT id FROM turns
    WHERE session_id = ?
      AND generation_id = ?
      -- 'leased' too: the context is registered at claim now, so a turn whose
      -- ack has not landed yet still has one and must have it renewed. Omitting
      -- it would let the context lapse underneath a turn that is starting.
      AND status IN ('leased', 'running')
  )`, formatTime(expiresAt), p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID, p.SessionID, p.GenerationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenewGenerationStartLease(ctx context.Context, p RenewGenerationStartLeaseParams) error {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.LeaseTTL <= 0 {
		return fmt.Errorf("lease ttl must be > 0")
	}
	expiresAt := p.Now.Add(p.LeaseTTL)
	res, err := s.db.ExecContext(ctx, `
UPDATE runtime_generations
SET lease_expires_at = ?,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('allocating','starting','probing','idle','active','restoring')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status NOT IN ('failed', 'destroyed')
  )`, formatTime(expiresAt), formatTime(p.Now), p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation start lease renewal CAS failed"); err != nil {
		return err
	}
	return nil
}

// FailGeneration keeps the historical error-only API for callers that do not
// need to publish the resulting durable event.
func (s *Store) FailGeneration(ctx context.Context, p FailGenerationParams) error {
	_, err := s.FailGenerationWithEvent(ctx, p)
	return err
}

// FailGenerationWithEvent atomically retires a failed generation, finishes the
// associated turn, reconciles the owning session, and records a retryable
// generation.error event.  The old implementation stopped after updating the
// turn/generation/resource rows, leaving sessions.running_active forever when a
// bridge protocol failure bypassed CompleteTurn.
func (s *Store) FailGenerationWithEvent(ctx context.Context, p FailGenerationParams) (FailGenerationResult, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FailGenerationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if p.TurnID != 0 {
		res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'failed',
    completed_at = ?,
    error_class = ?,
    error = ?
WHERE id = ?
  AND status IN ('leased', 'running')
  AND session_id = ?
  AND generation_id = ?
  AND lease_owner = ?
  AND lease_expires_at > ?`,
			formatTime(p.Now), nullableString(p.ErrorClass), nullableString(p.Reason), p.TurnID,
			p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now))
		if err != nil {
			return FailGenerationResult{}, err
		}
		if err := requireOneRow(res, "turn failure CAS failed"); err != nil {
			return FailGenerationResult{}, err
		}
	}
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = ?,
    failure_reason = ?,
    ended_at = ?,
    lease_owner = NULL
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('allocating','starting','probing','active','idle','checkpointing','restoring')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
  )`,
		nullableString(p.ErrorClass), nullableString(p.Reason), formatTime(p.Now), p.GenerationID, p.SessionID,
		p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return FailGenerationResult{}, err
	}
	if err := requireOneRow(res, "generation failure CAS failed"); err != nil {
		return FailGenerationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE generation_id = ?
		AND session_id = ?
		AND allocation_state IN ('allocating','ready','live','reserved_checkpointed','recreating')`, p.GenerationID, p.SessionID); err != nil {
		return FailGenerationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE generation_id = ?
		AND resource_state IN ('allocating','ready','live','reserved_checkpointed','recreating')`, p.GenerationID); err != nil {
		return FailGenerationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM active_model_request_contexts
		WHERE session_id = ?
		  AND generation_id = ?`, p.SessionID, p.GenerationID); err != nil {
		return FailGenerationResult{}, err
	}

	// A generation failure is retryable at the session level. Keep the session
	// input-blocking only when another turn is still queued/leased/running; once
	// no work remains it must become running_idle so the next user message can
	// allocate a replacement generation. Guard on active_generation_id so a
	// concurrent replacement cannot be overwritten.
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = CASE
      WHEN EXISTS (
        SELECT 1 FROM turns
        WHERE turns.session_id = sessions.id
          AND `+pendingTurnPredicateFor("turns")+`
      ) THEN 'running_active'
      ELSE 'running_idle'
    END,
    updated_at = ?,
    restore_ms = NULL,
    error_class = NULL,
    failure_reason = NULL
WHERE id = ?
  AND active_generation_id = ?
  AND status NOT IN ('failed', 'destroyed')`,
		formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return FailGenerationResult{}, err
	}
	if err := requireOneRow(res, "session generation failure CAS failed"); err != nil {
		return FailGenerationResult{}, err
	}

	var sessionStatus string
	if err := tx.QueryRowContext(ctx, `
SELECT status FROM sessions WHERE id = ? AND active_generation_id = ?`, p.SessionID, p.GenerationID).Scan(&sessionStatus); err != nil {
		return FailGenerationResult{}, err
	}
	eventID, err := appendEventTx(ctx, tx, AppendEventParams{
		SessionID:    p.SessionID,
		GenerationID: p.GenerationID,
		DedupeKey:    "generation_error:" + p.GenerationID,
		Type:         "generation.error",
		Payload: map[string]any{
			"terminal":             false,
			"error_class":          p.ErrorClass,
			"error":                p.Reason,
			"generation_id":        p.GenerationID,
			"session_status":       sessionStatus,
			"session_updated_at":   formatTime(p.Now),
			"active_generation_id": p.GenerationID,
		},
		Now: p.Now,
	})
	if err != nil {
		return FailGenerationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return FailGenerationResult{}, err
	}
	return FailGenerationResult{EventID: eventID, SessionStatus: sessionStatus}, nil
}

func (s *Store) FailGenerationStart(ctx context.Context, p FailGenerationStartParams) (int64, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.SessionStatus == "" {
		p.SessionStatus = "running_idle"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = ?,
    failure_reason = ?,
    ended_at = ?,
    lease_owner = NULL
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('allocating','starting','probing','idle','restoring')
  AND lease_owner = ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status NOT IN ('failed', 'destroyed')
  )`,
		nullableString(p.ErrorClass), nullableString(p.Reason), formatTime(p.Now), p.GenerationID, p.SessionID,
		p.Owner, p.SessionID, p.GenerationID)
	if err != nil {
		return 0, err
	}
	if err := requireOneRow(res, "generation start failure CAS failed"); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state IN ('allocating','ready','live','reserved_checkpointed','recreating')`, p.GenerationID, p.SessionID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE generation_id = ?
  AND resource_state IN ('allocating','ready','live','reserved_checkpointed','recreating')`, p.GenerationID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM active_model_request_contexts
WHERE session_id = ?
  AND generation_id = ?`, p.SessionID, p.GenerationID); err != nil {
		return 0, err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    restore_ms = NULL,
    error_class = NULL,
    failure_reason = NULL
WHERE id = ?
  AND active_generation_id = ?
  AND status NOT IN ('failed', 'destroyed')`,
		p.SessionStatus, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return 0, err
	}
	if err := requireOneRow(res, "session start failure CAS failed"); err != nil {
		return 0, err
	}
	var eventID int64
	if p.EventType != "" {
		eventID, err = appendEventTx(ctx, tx, AppendEventParams{
			SessionID:    p.SessionID,
			GenerationID: p.GenerationID,
			DedupeKey:    p.EventDedupeKey,
			Type:         p.EventType,
			Payload: map[string]any{
				"terminal":             false,
				"error_class":          p.ErrorClass,
				"error":                p.Reason,
				"generation_id":        p.GenerationID,
				"session_status":       p.SessionStatus,
				"session_updated_at":   formatTime(p.Now),
				"active_generation_id": p.GenerationID,
			},
			Now: p.Now,
		})
		if err != nil {
			return 0, err
		}
	}
	return eventID, tx.Commit()
}

func claimReplay(ctx context.Context, tx *sql.Tx, p ClaimNextTurnParams) (TurnGrant, bool, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id
FROM turns
WHERE session_id = ?
  AND generation_id = ?
  AND claim_request_id = ?
  AND status IN ('leased', 'running')
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM runtime_resource_instances ri
    JOIN runtime_generations g ON g.generation_id = ri.generation_id
    WHERE ri.generation_id = ?
      AND ri.state = 'live'
      AND ri.sandbox_contract_version = 'sandbox-isolation-v1'
      AND g.sandbox_contract_version = 'sandbox-isolation-v1'
      AND ri.contract_id = g.sandbox_contract_id
  )`,
		p.SessionID, p.GenerationID, p.RequestID, p.Owner, formatTime(p.Now), p.GenerationID)
	var turnID int64
	err := row.Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnGrant{}, false, nil
	}
	if err != nil {
		return TurnGrant{}, false, err
	}
	if _, err := getSandboxContractForGenerationWithMirrors(ctx, tx, p.SessionID, p.GenerationID); err != nil {
		return TurnGrant{}, false, err
	}
	grant, err := turnGrantByID(ctx, tx, p.SessionID, p.GenerationID, turnID, p.Owner, p.Now)
	if err != nil {
		return TurnGrant{}, false, err
	}
	return grant, true, nil
}

func turnGrantByID(ctx context.Context, tx *sql.Tx, sessionID, generationID string, turnID int64, owner string, now time.Time) (TurnGrant, error) {
	row := tx.QueryRowContext(ctx, `
SELECT t.id, t.sequence, t.content, t.attempt, t.lease_expires_at,
       ds.driver_id, ds.state_digest, ds.state_version, ds.state_payload
FROM turns t
JOIN runtime_generations g
  ON g.session_id = t.session_id
 AND g.generation_id = t.generation_id
JOIN agent_runtime_profiles a
  ON a.agent_runtime_profile_id = g.agent_runtime_profile_id
JOIN session_driver_states ds
  ON ds.session_id = t.session_id
 AND ds.driver_id = a.driver_id
WHERE t.id = ?
  AND t.session_id = ?
  AND t.generation_id = ?
  AND t.status IN ('leased', 'running')
  AND t.lease_owner = ?
  AND t.lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM runtime_resource_instances ri
    WHERE ri.generation_id = ?
      AND ri.state = 'live'
      AND ri.sandbox_contract_version = 'sandbox-isolation-v1'
      AND g.sandbox_contract_version = 'sandbox-isolation-v1'
      AND ri.contract_id = g.sandbox_contract_id
  )`,
		turnID, sessionID, generationID, owner, formatTime(now), generationID)
	var grant TurnGrant
	var leaseExpires string
	err := row.Scan(
		&grant.TurnID,
		&grant.Sequence,
		&grant.Content,
		&grant.Attempt,
		&leaseExpires,
		&grant.DriverState.DriverID,
		&grant.DriverState.StateDigest,
		&grant.DriverState.StateVersion,
		&grant.DriverStatePayload,
	)
	if err != nil {
		return TurnGrant{}, err
	}
	grant.ExpiresAt = parseTime(leaseExpires)
	return grant, nil
}

func nullableInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nextTurnSequence(ctx context.Context, tx *sql.Tx, sessionID string) (int64, error) {
	var next sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
SELECT MAX(sequence) + 1 FROM turns WHERE session_id = ?`, sessionID).Scan(&next); err != nil {
		return 0, err
	}
	if !next.Valid {
		return 1, nil
	}
	return next.Int64, nil
}

func markGenerationAndSessionIdleIfNoInflight(ctx context.Context, tx *sql.Tx, sessionID, generationID string, currentTurnID int64, owner string, now time.Time) (bool, error) {
	var ownsActiveGeneration int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
WHERE g.generation_id = ?
  AND g.session_id = ?
  AND g.status = 'active'
  AND g.lease_owner = ?
  AND g.lease_expires_at > ?
  AND s.id = ?
  AND s.active_generation_id = ?`,
		generationID, sessionID, owner, formatTime(now), sessionID, generationID).Scan(&ownsActiveGeneration); err != nil {
		return false, err
	}
	if ownsActiveGeneration != 1 {
		return false, permanentTurnCompletionf("generation idle CAS failed")
	}

	var pendingTurns int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE session_id = ?
  AND id <> ?
  AND `+pendingTurnPredicate+``, sessionID, currentTurnID).Scan(&pendingTurns); err != nil {
		return false, err
	}
	if pendingTurns != 0 {
		return false, nil
	}

	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle'
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'active'
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
  )
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE session_id = ?
      AND id <> ?
      AND `+pendingTurnPredicate+`
  )`, generationID, sessionID, owner, formatTime(now), sessionID, generationID, sessionID, currentTurnID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, permanentTurnCompletionf("generation idle CAS failed")
	}
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'running_idle',
    updated_at = ?,
    last_activity_at = ?
WHERE id = ?
  AND status NOT IN ('failed', 'destroyed')
  AND active_generation_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE session_id = ?
      AND id <> ?
      AND `+pendingTurnPredicate+`
  )`, formatTime(now), formatTime(now), sessionID, generationID, sessionID, currentTurnID)
	if err != nil {
		return false, err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, permanentTurnCompletionf("session idle CAS failed")
	}
	return true, nil
}
