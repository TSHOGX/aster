package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/canonicaljson"
)

const (
	driverStateDigestPrefix = "driver_state_digest_v1\n"
	emptyDriverStateKind    = "empty"
)

type DriverStateToken struct {
	DriverID     string `json:"driver_id"`
	StateDigest  string `json:"state_digest"`
	StateVersion int    `json:"state_version"`
}

type DriverStateUpdate struct {
	DriverID            string          `json:"driver_id"`
	PreviousStateDigest string          `json:"previous_state_digest"`
	StatePayload        json.RawMessage `json:"state_payload"`
	StateDigest         string          `json:"state_digest"`
	StateVersion        int             `json:"state_version"`
}

type driverStateRow struct {
	SessionID           string
	DriverID            string
	StatePayload        []byte
	StateDigest         string
	StateVersion        int
	UpdatedGenerationID string
	UpdatedTurnID       sql.NullInt64
	UpdatedAt           time.Time
}

type driverStateBootstrapContext struct{}

type driverStateCodec struct {
	driverID                    agents.ID
	bootstrap                   func(driverStateBootstrapContext) (any, error)
	validatePayload             func(map[string]any) error
	synthesizeCompletedUpdateTx func(context.Context, *sql.Tx, CompleteTurnParams, driverStateRow) error
}

func driverStateCodecRegistry() map[agents.ID]driverStateCodec {
	return map[agents.ID]driverStateCodec{
		agents.Pi: {
			driverID:        agents.Pi,
			bootstrap:       bootstrapPiDriverState,
			validatePayload: validatePiDriverStatePayload,
		},
	}
}

func driverStateCodecFor(driverID string) (driverStateCodec, bool) {
	codec, ok := driverStateCodecRegistry()[agents.ID(strings.TrimSpace(driverID))]
	return codec, ok
}

func DriverStateDigest(canonicalPayload []byte) string {
	sum := sha256.Sum256(append([]byte(driverStateDigestPrefix), canonicalPayload...))
	return "sha256:" + fmt.Sprintf("%x", sum[:])
}

func canonicalBootstrapDriverState(driverID string) ([]byte, string, error) {
	codec, ok := driverStateCodecFor(driverID)
	if !ok {
		return nil, "", fmt.Errorf("unsupported driver %q", driverID)
	}
	payload, err := codec.bootstrap(driverStateBootstrapContext{})
	if err != nil {
		return nil, "", err
	}
	return canonicalDriverStatePayload(payload, string(codec.driverID))
}

func canonicalDriverStatePayload(value any, driverID string) ([]byte, string, error) {
	var canonical []byte
	var err error
	switch v := value.(type) {
	case []byte:
		canonical, err = canonicaljson.Bytes(v)
	case json.RawMessage:
		canonical, err = canonicaljson.Bytes(v)
	default:
		canonical, err = canonicaljson.Marshal(value)
	}
	if err != nil {
		return nil, "", err
	}
	if err := validateDriverStatePayload(canonical, driverID); err != nil {
		return nil, "", err
	}
	return canonical, DriverStateDigest(canonical), nil
}

func validateDriverStatePayload(canonicalPayload []byte, driverID string) error {
	object, err := decodeDriverStateObject(canonicalPayload)
	if err != nil {
		return err
	}
	schemaVersion, ok := object["schema_version"].(json.Number)
	if !ok || schemaVersion.String() != "1" {
		return fmt.Errorf("driver state schema_version must be 1")
	}
	if got, _ := object["driver_id"].(string); got != strings.TrimSpace(driverID) {
		return fmt.Errorf("driver state driver_id = %q, want %q", got, strings.TrimSpace(driverID))
	}
	codec, ok := driverStateCodecFor(driverID)
	if !ok {
		return fmt.Errorf("unsupported driver %q", driverID)
	}
	return codec.validatePayload(object)
}

func validateEmptyDriverStatePayload(object map[string]any) error {
	if got, _ := object["state_kind"].(string); got != emptyDriverStateKind {
		return fmt.Errorf("shell driver state_kind = %q", got)
	}
	return nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func ensureAllocationDriverStateTx(ctx context.Context, tx *sql.Tx, sessionID, generationID, driverID string, now time.Time) (DriverStateToken, error) {
	row, err := getDriverStateTx(ctx, tx, sessionID, driverID)
	if err == nil {
		return DriverStateToken{DriverID: row.DriverID, StateDigest: row.StateDigest, StateVersion: row.StateVersion}, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return DriverStateToken{}, err
	}
	var priorGenerations int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM runtime_generations g
LEFT JOIN execution_bindings b ON b.generation_id=g.generation_id
WHERE g.session_id = ?
  AND b.driver_id = ?
  AND g.generation_id <> ?`, sessionID, driverID, generationID).Scan(&priorGenerations); err != nil {
		return DriverStateToken{}, err
	}
	if priorGenerations != 0 {
		return DriverStateToken{}, fmt.Errorf("missing driver state for existing %s session", driverID)
	}
	payload, digest, err := canonicalBootstrapDriverState(driverID)
	if err != nil {
		return DriverStateToken{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO session_driver_states (
  session_id, driver_id, state_payload, state_digest, state_version,
  updated_generation_id, updated_turn_id, updated_at
) VALUES (?, ?, ?, ?, 1, ?, NULL, ?)`,
		sessionID, driverID, string(payload), digest, generationID, formatTime(now)); err != nil {
		return DriverStateToken{}, err
	}
	return DriverStateToken{DriverID: driverID, StateDigest: digest, StateVersion: 1}, nil
}

func getDriverStateTx(ctx context.Context, tx *sql.Tx, sessionID, driverID string) (driverStateRow, error) {
	row := tx.QueryRowContext(ctx, `
SELECT
  session_id, driver_id, state_payload, state_digest, state_version,
  updated_generation_id, updated_turn_id, updated_at
FROM session_driver_states
WHERE session_id = ?
  AND driver_id = ?`, sessionID, driverID)
	var state driverStateRow
	var payload, updatedAt string
	if err := row.Scan(
		&state.SessionID,
		&state.DriverID,
		&payload,
		&state.StateDigest,
		&state.StateVersion,
		&state.UpdatedGenerationID,
		&state.UpdatedTurnID,
		&updatedAt,
	); err != nil {
		return driverStateRow{}, err
	}
	state.StatePayload = []byte(payload)
	state.UpdatedAt = parseTime(updatedAt)
	if err := validateDriverStateRow(state); err != nil {
		return driverStateRow{}, err
	}
	return state, nil
}

func validateDriverStateRow(state driverStateRow) error {
	canonical, digest, err := canonicalDriverStatePayload(state.StatePayload, state.DriverID)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, state.StatePayload) {
		return fmt.Errorf("driver state payload is not canonical")
	}
	if digest != state.StateDigest {
		return fmt.Errorf("driver state digest mismatch: got %s want %s", digest, state.StateDigest)
	}
	if state.StateVersion <= 0 {
		return fmt.Errorf("driver state version must be positive")
	}
	return nil
}

func currentDriverStateTokenTx(ctx context.Context, tx *sql.Tx, sessionID, generationID string) (DriverStateToken, error) {
	var driverID string
	if err := tx.QueryRowContext(ctx, `
SELECT b.driver_id
FROM runtime_generations g
LEFT JOIN execution_bindings b ON b.generation_id=g.generation_id
WHERE g.session_id = ?
  AND g.generation_id = ?`, sessionID, generationID).Scan(&driverID); err != nil {
		return DriverStateToken{}, err
	}
	state, err := getDriverStateTx(ctx, tx, sessionID, driverID)
	if err != nil {
		return DriverStateToken{}, err
	}
	return DriverStateToken{DriverID: driverID, StateDigest: state.StateDigest, StateVersion: state.StateVersion}, nil
}

func applyDriverStateUpdateTx(ctx context.Context, tx *sql.Tx, p CompleteTurnParams) error {
	if p.DriverStateUpdate == nil {
		return synthesizeCompletedDriverStateUpdateTx(ctx, tx, p)
	}
	update := *p.DriverStateUpdate
	driverID, err := agents.CanonicalDriverID(update.DriverID)
	if err != nil {
		return permanentTurnCompletion(err)
	}
	if p.TerminalStatus != "completed" {
		return permanentTurnCompletionf("driver state update is only valid for completed turns")
	}
	canonical, nextDigest, err := canonicalDriverStatePayload(update.StatePayload, string(driverID))
	if err != nil {
		return permanentTurnCompletion(err)
	}
	if update.StateDigest != nextDigest {
		return permanentTurnCompletionf("driver state digest mismatch: got %s want %s", update.StateDigest, nextDigest)
	}

	var generationDriver string
	if err := tx.QueryRowContext(ctx, `
SELECT b.driver_id
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
LEFT JOIN execution_bindings b ON b.generation_id=g.generation_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND g.lease_owner = ?
  AND g.lease_expires_at > ?
  AND s.active_generation_id = ?`,
		p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now), p.GenerationID).Scan(&generationDriver); err != nil {
		return fmt.Errorf("driver state generation lease check: %w", err)
	}
	if generationDriver != string(driverID) {
		return permanentTurnCompletionf("driver state update driver mismatch: got %s want %s", driverID, generationDriver)
	}
	var turnCount int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE id = ?
  AND session_id = ?
  AND generation_id = ?
  AND lease_owner = ?`,
		p.TurnID, p.SessionID, p.GenerationID, p.Owner).Scan(&turnCount); err != nil {
		return err
	}
	if turnCount != 1 {
		return permanentTurnCompletionf("driver state update turn does not belong to generation")
	}
	if driverID == agents.Pi {
		if err := assertDriverStateEvidenceTx(ctx, tx, p, nextDigest); err != nil {
			return err
		}
	}

	current, err := getDriverStateTx(ctx, tx, p.SessionID, string(driverID))
	if err != nil {
		return err
	}
	if current.StateDigest != strings.TrimSpace(update.PreviousStateDigest) {
		return driverStateReplayOrStale(ctx, tx, p, current, canonical, update)
	}
	if update.StateVersion != current.StateVersion+1 {
		return permanentTurnCompletionf("driver state version = %d, want %d", update.StateVersion, current.StateVersion+1)
	}
	if driverID == agents.Pi && p.DriverStateEvidence != nil && p.DriverStateEvidence.NativeSnapshotPath != "" {
		if err := recordNativeCaptureRequestTx(ctx, tx, p, canonical); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `
UPDATE session_driver_states
SET state_payload = ?,
    state_digest = ?,
    state_version = ?,
    updated_generation_id = ?,
    updated_turn_id = ?,
    updated_at = ?
WHERE session_id = ?
  AND driver_id = ?
  AND state_digest = ?
  AND state_version = ?`,
		string(canonical), update.StateDigest, update.StateVersion, p.GenerationID, p.TurnID, formatTime(p.Now),
		p.SessionID, string(driverID), update.PreviousStateDigest, current.StateVersion)
	if err != nil {
		return err
	}
	return permanentRequireOneRow(res, "driver state CAS failed")
}

func synthesizeCompletedDriverStateUpdateTx(ctx context.Context, tx *sql.Tx, p CompleteTurnParams) error {
	if p.TerminalStatus != "completed" {
		return nil
	}
	var driverID string
	if err := tx.QueryRowContext(ctx, `
SELECT b.driver_id
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
LEFT JOIN execution_bindings b ON b.generation_id=g.generation_id
WHERE g.session_id = ?
  AND g.generation_id = ?
  AND g.lease_owner = ?
  AND g.lease_expires_at > ?
  AND s.active_generation_id = ?`,
		p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now), p.GenerationID).Scan(&driverID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("driver state generation lease check: %w", err)
	}
	codec, ok := driverStateCodecFor(driverID)
	if !ok || codec.synthesizeCompletedUpdateTx == nil {
		return nil
	}
	current, err := getDriverStateTx(ctx, tx, p.SessionID, driverID)
	if err != nil {
		return err
	}
	return codec.synthesizeCompletedUpdateTx(ctx, tx, p, current)
}

func driverStateReplayOrStale(ctx context.Context, tx *sql.Tx, p CompleteTurnParams, current driverStateRow, canonical []byte, update DriverStateUpdate) error {
	if current.StateDigest == update.StateDigest &&
		current.StateVersion == update.StateVersion &&
		current.UpdatedGenerationID == p.GenerationID &&
		current.UpdatedTurnID.Valid &&
		current.UpdatedTurnID.Int64 == p.TurnID &&
		bytes.Equal(current.StatePayload, canonical) {
		var alreadyTerminal int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM turns
WHERE id = ?
  AND session_id = ?
  AND generation_id = ?
  AND status = ?
  AND completed_by_generation = ?`,
			p.TurnID, p.SessionID, p.GenerationID, p.TerminalStatus, p.GenerationID).Scan(&alreadyTerminal); err != nil {
			return err
		}
		if alreadyTerminal == 1 {
			return nil
		}
	}
	return permanentTurnCompletionf("driver state stale digest: got %s want %s", update.PreviousStateDigest, current.StateDigest)
}

// CanonicalDriverStatePayload validates portable state metadata without I/O.
func CanonicalDriverStatePayload(value any, driverID string) ([]byte, string, error) {
	return canonicalDriverStatePayload(value, driverID)
}

func decodeDriverStateObject(raw []byte) (map[string]any, error) {
	var obj map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err := decoder.Decode(&obj)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("driver state object required")
	}
	return obj, nil
}
