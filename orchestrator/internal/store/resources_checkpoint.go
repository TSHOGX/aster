package store

// This file holds the checkpoint and restore half of the generation lifecycle,
// split out of resources.go (which was ~2455 lines). The split follows the
// interface grouping: these are the methods behind the checkpoint/restore
// concern -- claiming a checkpointed generation for restore, retiring expired
// checkpoints and restore fallbacks, and the begin/abort/complete checkpoint
// transitions -- while resources.go keeps allocation, start/live transitions,
// leases, and reaping. Declarations were moved verbatim; no SQL changed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) ClaimCheckpointedGenerationForRestore(ctx context.Context, p ClaimCheckpointedGenerationParams) (GenerationAllocation, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return GenerationAllocation{}, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(p.GenerationID) == "" {
		return GenerationAllocation{}, fmt.Errorf("generation id is required")
	}
	if strings.TrimSpace(p.Owner) == "" {
		return GenerationAllocation{}, fmt.Errorf("owner is required")
	}
	if p.LeaseTTL <= 0 {
		return GenerationAllocation{}, fmt.Errorf("lease ttl must be > 0")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GenerationAllocation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return GenerationAllocation{}, err
	}
	currentFence, err := checkpointDriverStatesDigestTx(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return GenerationAllocation{}, err
	}
	var storedFence string
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(checkpoint_driver_states_digest, '')
FROM runtime_generations
WHERE generation_id = ?
  AND session_id = ?`, p.GenerationID, p.SessionID).Scan(&storedFence); err != nil {
		return GenerationAllocation{}, err
	}
	if storedFence == "" {
		return GenerationAllocation{}, fmt.Errorf("%w: checkpoint driver state fence is missing", ErrStaleCheckpointRestore)
	}
	if storedFence != currentFence {
		return GenerationAllocation{}, fmt.Errorf("%w: checkpoint driver state fence mismatch", ErrStaleCheckpointRestore)
	}

	expiresAt := p.Now.Add(p.LeaseTTL)
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'restoring',
    lease_owner = ?,
    lease_expires_at = ?,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'checkpointed'
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status = 'checkpointed'
  )`, p.Owner, formatTime(expiresAt), formatTime(p.Now), p.GenerationID, p.SessionID, p.SessionID, p.GenerationID)
	if err != nil {
		return GenerationAllocation{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return GenerationAllocation{}, err
	}
	if affected != 1 {
		stale, err := staleCheckpointRestoreTx(ctx, tx, p.SessionID, p.GenerationID)
		if err != nil {
			return GenerationAllocation{}, err
		}
		if stale {
			return GenerationAllocation{}, fmt.Errorf("%w: checkpointed generation restore CAS failed", ErrStaleCheckpointRestore)
		}
		return GenerationAllocation{}, fmt.Errorf("checkpointed generation restore CAS failed")
	}

	res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'recreating'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state = 'reserved_checkpointed'`, p.GenerationID, p.SessionID)
	if err != nil {
		return GenerationAllocation{}, err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return GenerationAllocation{}, err
	}
	if affected != 1 {
		return GenerationAllocation{}, fmt.Errorf("checkpointed network restore CAS failed")
	}

	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'recreating'
WHERE generation_id = ?
  AND resource_state = 'reserved_checkpointed'`, p.GenerationID)
	if err != nil {
		return GenerationAllocation{}, err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return GenerationAllocation{}, err
	}
	if affected != 1 {
		return GenerationAllocation{}, fmt.Errorf("checkpointed resource restore CAS failed")
	}

	allocation := GenerationAllocation{
		GenerationID:   p.GenerationID,
		Owner:          p.Owner,
		LeaseExpiresAt: expiresAt,
	}
	if err := tx.QueryRowContext(ctx, `
SELECT network_profile_id, agent_runtime_profile_id
FROM runtime_generations
WHERE generation_id = ?
  AND session_id = ?`, p.GenerationID, p.SessionID).Scan(&allocation.NetworkProfileID, &allocation.AgentRuntimeProfileID); err != nil {
		return GenerationAllocation{}, err
	}
	driverState, err := currentDriverStateTokenTx(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return GenerationAllocation{}, err
	}
	allocation.DriverState = driverState
	if err := tx.Commit(); err != nil {
		return GenerationAllocation{}, err
	}
	return allocation, nil
}

func staleCheckpointRestoreTx(ctx context.Context, tx *sql.Tx, sessionID, generationID string) (bool, error) {
	var sessionStatus, activeGenerationID, generationStatus string
	if err := tx.QueryRowContext(ctx, `
SELECT s.status, COALESCE(s.active_generation_id, ''), COALESCE(g.status, '')
FROM sessions s
LEFT JOIN runtime_generations g ON g.session_id = s.id
  AND g.generation_id = ?
WHERE s.id = ?`, generationID, sessionID).Scan(&sessionStatus, &activeGenerationID, &generationStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return activeGenerationID != generationID ||
		sessionStatus != "checkpointed" ||
		generationStatus != "checkpointed", nil
}

func (s *Store) RetireExpiredCheckpoints(ctx context.Context, p RetireExpiredCheckpointsParams) ([]RetiredCheckpoint, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.CheckpointImageRetention < 0 {
		return nil, fmt.Errorf("checkpoint image retention must be >= 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, p.OwnerUUID); err != nil {
		return nil, err
	}

	cutoff := p.Now.Add(-p.CheckpointImageRetention)
	rows, err := tx.QueryContext(ctx, `
SELECT s.id, g.generation_id, s.last_activity_at
FROM sessions s
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
  AND g.session_id = s.id
JOIN network_profiles n ON n.generation_id = g.generation_id
  AND n.session_id = s.id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE s.status = 'checkpointed'
  AND g.status = 'checkpointed'
  AND n.allocation_state = 'reserved_checkpointed'
  AND r.resource_state = 'reserved_checkpointed'
  AND COALESCE(s.last_activity_at, g.checkpoint_created_at, s.updated_at, s.created_at) < ?
ORDER BY COALESCE(s.last_activity_at, g.checkpoint_created_at, s.updated_at, s.created_at), s.id`, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	type candidate struct {
		sessionID      string
		generationID   string
		lastActivityAt sql.NullString
	}
	var candidates []candidate
	for rows.Next() {
		var candidate candidate
		if err := rows.Scan(&candidate.sessionID, &candidate.generationID, &candidate.lastActivityAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	retired := make([]RetiredCheckpoint, 0, len(candidates))
	nowString := formatTime(p.Now)
	for _, candidate := range candidates {
		res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = 'checkpoint_retired',
    failure_reason = 'checkpoint image retired after retention window',
    ended_at = ?,
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'checkpointed'
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status = 'checkpointed'
  )`, nowString, candidate.generationID, candidate.sessionID, candidate.sessionID, candidate.generationID)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "checkpoint retirement generation CAS failed"); err != nil {
			return nil, err
		}
		res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state = 'reserved_checkpointed'`, candidate.generationID, candidate.sessionID)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "checkpoint retirement network CAS failed"); err != nil {
			return nil, err
		}
		res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE generation_id = ?
  AND resource_state = 'reserved_checkpointed'`, candidate.generationID)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "checkpoint retirement resource CAS failed"); err != nil {
			return nil, err
		}
		res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'running_idle',
    checkpoint_path = NULL,
    restore_ms = NULL,
    updated_at = ?
WHERE id = ?
  AND status = 'checkpointed'
  AND active_generation_id = ?`, nowString, candidate.sessionID, candidate.generationID)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(res, "checkpoint retirement session CAS failed"); err != nil {
			return nil, err
		}
		var lastActivity any
		if candidate.lastActivityAt.Valid {
			lastActivity = candidate.lastActivityAt.String
		}
		eventID, err := appendEventTx(ctx, tx, AppendEventParams{
			SessionID:    candidate.sessionID,
			GenerationID: candidate.generationID,
			DedupeKey:    "checkpoint_retired:" + candidate.generationID,
			Type:         "session.checkpoint_retired",
			Payload: map[string]any{
				"terminal":                 false,
				"generation_id":            candidate.generationID,
				"session_status":           "running_idle",
				"status":                   "running_idle",
				"session_updated_at":       nowString,
				"updated_at":               nowString,
				"session_last_activity_at": lastActivity,
				"active_generation_id":     candidate.generationID,
				"restore_ms":               nil,
			},
			Now: p.Now,
		})
		if err != nil {
			return nil, err
		}
		retired = append(retired, RetiredCheckpoint{SessionID: candidate.sessionID, GenerationID: candidate.generationID, EventID: eventID})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return retired, nil
}

func (s *Store) RetireRestoreFallback(ctx context.Context, p RetireRestoreFallbackParams) (RetiredRestoreFallback, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return RetiredRestoreFallback{}, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(p.GenerationID) == "" {
		return RetiredRestoreFallback{}, fmt.Errorf("generation id is required")
	}
	if strings.TrimSpace(p.Owner) == "" {
		return RetiredRestoreFallback{}, fmt.Errorf("owner is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(p.Owner)); err != nil {
		return RetiredRestoreFallback{}, err
	}

	var lastActivityAt sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT last_activity_at
FROM sessions
WHERE id = ?
  AND active_generation_id = ?
  AND status = 'checkpointed'`, p.SessionID, p.GenerationID).Scan(&lastActivityAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RetiredRestoreFallback{}, fmt.Errorf("restore fallback session CAS failed")
		}
		return RetiredRestoreFallback{}, err
	}

	nowString := formatTime(p.Now)
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = ?,
    failure_reason = ?,
    ended_at = ?,
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE generation_id = ?
  AND session_id = ?
  AND status IN ('checkpointed','restoring')
  AND (
    (status = 'restoring' AND lease_owner = ? AND lease_expires_at > ?)
    OR (status = 'checkpointed' AND (lease_owner IS NULL OR lease_owner = ''))
  )
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status = 'checkpointed'
  )`,
		nullableString(p.ErrorClass), nullableString(p.Reason), nowString, p.GenerationID, p.SessionID,
		p.Owner, nowString, p.SessionID, p.GenerationID)
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	if err := requireOneRow(res, "restore fallback generation CAS failed"); err != nil {
		return RetiredRestoreFallback{}, err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state IN ('reserved_checkpointed','recreating')`, p.GenerationID, p.SessionID)
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	if err := requireOneRow(res, "restore fallback network CAS failed"); err != nil {
		return RetiredRestoreFallback{}, err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE generation_id = ?
  AND resource_state IN ('reserved_checkpointed','recreating')`, p.GenerationID)
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	if err := requireOneRow(res, "restore fallback resource CAS failed"); err != nil {
		return RetiredRestoreFallback{}, err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'running_idle',
    checkpoint_path = NULL,
    restore_ms = NULL,
    updated_at = ?,
    error_class = NULL,
    failure_reason = NULL
WHERE id = ?
  AND status = 'checkpointed'
  AND active_generation_id = ?`, nowString, p.SessionID, p.GenerationID)
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	if err := requireOneRow(res, "restore fallback session update CAS failed"); err != nil {
		return RetiredRestoreFallback{}, err
	}
	var lastActivity any
	if lastActivityAt.Valid {
		lastActivity = lastActivityAt.String
	}
	statusEventID, err := appendEventTx(ctx, tx, AppendEventParams{
		SessionID:    p.SessionID,
		GenerationID: p.GenerationID,
		DedupeKey:    "restore_fallback_retired:" + p.GenerationID,
		Type:         "session.restore_fallback_retired",
		Payload: map[string]any{
			"terminal":                 false,
			"generation_id":            p.GenerationID,
			"session_status":           "running_idle",
			"status":                   "running_idle",
			"session_updated_at":       nowString,
			"updated_at":               nowString,
			"session_last_activity_at": lastActivity,
			"active_generation_id":     p.GenerationID,
			"restore_ms":               nil,
			"fallback":                 "cold",
			"error_class":              p.ErrorClass,
			"error":                    p.Reason,
		},
		Now: p.Now,
	})
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	errorEventID, err := appendEventTx(ctx, tx, AppendEventParams{
		SessionID:    p.SessionID,
		GenerationID: p.GenerationID,
		DedupeKey:    "restore_fallback_error:" + p.GenerationID,
		Type:         "generation.error",
		Payload: map[string]any{
			"terminal":             false,
			"error_class":          p.ErrorClass,
			"error":                p.Reason,
			"generation_id":        p.GenerationID,
			"session_status":       "running_idle",
			"session_updated_at":   nowString,
			"active_generation_id": p.GenerationID,
			"fallback":             "cold",
		},
		Now: p.Now,
	})
	if err != nil {
		return RetiredRestoreFallback{}, err
	}
	if err := tx.Commit(); err != nil {
		return RetiredRestoreFallback{}, err
	}
	return RetiredRestoreFallback{
		SessionID:    p.SessionID,
		GenerationID: p.GenerationID,
		EventIDs:     []int64{statusEventID, errorEventID},
	}, nil
}

func (s *Store) ListAutoCheckpointCandidates(ctx context.Context, owner string, now time.Time, idleThreshold time.Duration) ([]CheckpointCandidate, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("owner is required")
	}
	if idleThreshold < 0 {
		return nil, fmt.Errorf("idle threshold must be >= 0")
	}
	cutoff := now.Add(-idleThreshold)
	// As in ListBridgePollGenerations, the sandbox_contracts JOIN folds contract
	// validation into this query so the maintenance tick keeps the corrupt-contract
	// gate without an extra query per candidate. INNER JOIN is correct for the same
	// reason: the ri.state='live' filter guarantees a contract row exists.
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, g.generation_id, r.bridge_dir_path,
  sc.contract_id,
  sc.session_id,
  sc.generation_id,
  sc.sandbox_contract_version,
  sc.contract_schema_version,
  sc.contract_gate_version,
  sc.canonical_payload,
  sc.sandbox_contract_digest,
  sc.created_at,
  COALESCE(g.sandbox_contract_id, ''),
  COALESCE(g.sandbox_contract_version, ''),
  COALESCE(r.contract_id, ''),
  COALESCE(r.sandbox_contract_version, '')
FROM sessions s
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
  AND g.session_id = s.id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN runtime_resource_instances ri ON ri.generation_id = g.generation_id
  AND ri.session_id = g.session_id
JOIN sandbox_contracts sc ON sc.generation_id = g.generation_id
WHERE s.status = 'running_idle'
  AND s.auto_checkpoint_enabled = 1
  AND s.last_activity_at IS NOT NULL
  AND s.last_activity_at <= ?
  AND g.status = 'idle'
  AND g.auto_checkpoint_enabled = 1
  AND g.lease_owner = ?
  AND g.lease_expires_at > ?
  AND g.runsc_version IS NOT NULL
  AND g.runsc_platform IS NOT NULL
  AND r.resource_state = 'live'
  AND ri.state = 'live'
  AND r.checkpoint_path IS NOT NULL
  AND r.control_manifest_digest IS NOT NULL
  AND r.projected_control_manifest_digest IS NOT NULL
  AND r.bundle_digest IS NOT NULL
  AND r.runtime_config_digest IS NOT NULL
  AND r.spec_digest IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM turns t
    WHERE t.session_id = s.id
      AND t.status IN ('queued', 'leased', 'running')
  )
ORDER BY s.last_activity_at ASC`, formatTime(cutoff), owner, formatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []CheckpointCandidate
	for rows.Next() {
		var (
			candidate                                       CheckpointCandidate
			record                                          SandboxContractRecord
			payload, createdAt                              string
			generationContractID, generationContractVersion string
			resourceContractID, resourceContractVersion     string
		)
		if err := rows.Scan(
			&candidate.SessionID, &candidate.GenerationID, &candidate.BridgeDirPath,
			&record.ContractID,
			&record.SessionID,
			&record.GenerationID,
			&record.SandboxContractVersion,
			&record.ContractSchemaVersion,
			&record.ContractGateVersion,
			&payload,
			&record.SandboxContractDigest,
			&createdAt,
			&generationContractID,
			&generationContractVersion,
			&resourceContractID,
			&resourceContractVersion,
		); err != nil {
			return nil, err
		}
		if _, err := validateScannedSandboxContractWithMirrors(
			record, payload, createdAt,
			generationContractID, generationContractVersion,
			resourceContractID, resourceContractVersion,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// ListIdleReleaseCandidates lists idle sessions whose sandbox should be
// released (torn down) to free compute. Unlike ListAutoCheckpointCandidates it
// does not require any checkpoint-image artifacts (checkpoint_path, manifest or
// bundle digests): releasing takes no runsc checkpoint, so the only
// requirements are an idle, auto-checkpoint-enabled generation that still owns
// a live resource, holds a valid lease, and has no in-flight turn.
func (s *Store) ListIdleReleaseCandidates(ctx context.Context, owner string, now time.Time, idleThreshold time.Duration) ([]CheckpointCandidate, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("owner is required")
	}
	if idleThreshold < 0 {
		return nil, fmt.Errorf("idle threshold must be >= 0")
	}
	cutoff := now.Add(-idleThreshold)
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, g.generation_id, r.bridge_dir_path
FROM sessions s
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
  AND g.session_id = s.id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
JOIN runtime_resource_instances ri ON ri.generation_id = g.generation_id
  AND ri.session_id = g.session_id
WHERE s.status = 'running_idle'
  AND s.auto_checkpoint_enabled = 1
  AND s.last_activity_at IS NOT NULL
  AND s.last_activity_at <= ?
  AND g.status = 'idle'
  AND g.auto_checkpoint_enabled = 1
  AND g.lease_owner = ?
  AND g.lease_expires_at > ?
  AND r.resource_state = 'live'
  AND ri.state = 'live'
  AND NOT EXISTS (
    SELECT 1 FROM turns t
    WHERE t.session_id = s.id
      AND t.status IN ('queued', 'leased', 'running')
  )
ORDER BY s.last_activity_at ASC`, formatTime(cutoff), owner, formatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []CheckpointCandidate
	for rows.Next() {
		var candidate CheckpointCandidate
		if err := rows.Scan(&candidate.SessionID, &candidate.GenerationID, &candidate.BridgeDirPath); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Store) BeginGenerationCheckpoint(ctx context.Context, sessionID, generationID, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := getSandboxContractForGenerationWithMirrors(ctx, tx, sessionID, generationID); err != nil {
		return err
	}
	checkpointDriverStatesDigest, err := checkpointDriverStatesDigestTx(ctx, tx, sessionID, generationID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'checkpointing',
    last_seen_at = ?,
    checkpoint_driver_states_digest = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'idle'
  AND auto_checkpoint_enabled = 1
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND runsc_version IS NOT NULL
  AND runsc_platform IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status = 'running_idle'
      AND auto_checkpoint_enabled = 1
  )
  AND EXISTS (
    SELECT 1 FROM runtime_generation_resources r
    WHERE r.generation_id = runtime_generations.generation_id
      AND r.resource_state = 'live'
      AND r.checkpoint_path IS NOT NULL
      AND r.control_manifest_digest IS NOT NULL
      AND r.projected_control_manifest_digest IS NOT NULL
      AND r.bundle_digest IS NOT NULL
      AND r.runtime_config_digest IS NOT NULL
      AND r.spec_digest IS NOT NULL
  )
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE session_id = ?
      AND status IN ('queued', 'leased', 'running')
  )`, formatTime(now), checkpointDriverStatesDigest, generationID, sessionID, owner, formatTime(now), sessionID, generationID, sessionID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation checkpoint begin CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'checkpointing',
    updated_at = ?
WHERE id = ?
  AND status = 'running_idle'
  AND active_generation_id = ?
  AND auto_checkpoint_enabled = 1`, formatTime(now), sessionID, generationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "session checkpoint begin CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AbortGenerationCheckpoint(ctx context.Context, sessionID, generationID, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    last_seen_at = ?,
    checkpoint_driver_states_digest = NULL
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'checkpointing'
  AND lease_owner = ?
  AND lease_expires_at > ?`, formatTime(now), generationID, sessionID, owner, formatTime(now))
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation checkpoint abort CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'running_idle',
    updated_at = ?
WHERE id = ?
  AND status = 'checkpointing'
  AND active_generation_id = ?`, formatTime(now), sessionID, generationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "session checkpoint abort CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteGenerationCheckpoint(ctx context.Context, p CompleteCheckpointParams) error {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.CheckpointPath) == "" {
		return fmt.Errorf("checkpoint path is required")
	}
	if strings.TrimSpace(p.CheckpointBundleDigest) == "" {
		return fmt.Errorf("checkpoint bundle digest is required")
	}
	if strings.TrimSpace(p.CheckpointRuntimeConfigDigest) == "" {
		return fmt.Errorf("checkpoint runtime config digest is required")
	}
	if strings.TrimSpace(p.CheckpointControlManifestDigest) == "" {
		return fmt.Errorf("checkpoint control manifest digest is required")
	}
	if strings.TrimSpace(p.RunscBinaryPath) == "" {
		return fmt.Errorf("checkpoint runsc binary path is required")
	}
	if strings.TrimSpace(p.RunscBinaryDigest) == "" {
		return fmt.Errorf("checkpoint runsc binary digest is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	currentFence, err := checkpointDriverStatesDigestTx(ctx, tx, p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	var storedFence string
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(checkpoint_driver_states_digest, '')
FROM runtime_generations
WHERE generation_id = ?
  AND session_id = ?`, p.GenerationID, p.SessionID).Scan(&storedFence); err != nil {
		return err
	}
	if storedFence == "" {
		return fmt.Errorf("checkpoint driver state fence is missing")
	}
	if storedFence != currentFence {
		return fmt.Errorf("checkpoint driver state fence mismatch")
	}
	res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'checkpointed',
    checkpoint_created_at = ?,
    checkpoint_network_profile_id = network_profile_id,
    checkpoint_agent_runtime_profile_id = agent_runtime_profile_id,
    checkpoint_runsc_version = COALESCE(?, runsc_version),
    checkpoint_runsc_platform = COALESCE(?, runsc_platform),
    checkpoint_runsc_binary_path = ?,
    checkpoint_runsc_binary_digest = ?,
    checkpoint_bundle_digest = ?,
    checkpoint_runtime_config_digest = ?,
    checkpoint_control_manifest_digest = ?,
    lease_owner = NULL,
    lease_expires_at = NULL,
    last_seen_at = ?
WHERE generation_id = ?
  AND session_id = ?
  AND status = 'checkpointing'
  AND lease_owner = ?
  AND lease_expires_at > ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE id = ?
      AND active_generation_id = ?
      AND status = 'checkpointing'
  )`, formatTime(p.Now), nullableString(p.RunscVersion), nullableString(p.RunscPlatform), p.RunscBinaryPath, p.RunscBinaryDigest,
		p.CheckpointBundleDigest, p.CheckpointRuntimeConfigDigest, p.CheckpointControlManifestDigest,
		formatTime(p.Now), p.GenerationID, p.SessionID, p.Owner, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "generation checkpoint complete CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reserved_checkpointed'
WHERE generation_id = ?
  AND session_id = ?
  AND allocation_state = 'live'`, p.GenerationID, p.SessionID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "network checkpoint complete CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reserved_checkpointed',
    checkpoint_path = ?
WHERE generation_id = ?
  AND resource_state = 'live'`, p.CheckpointPath, p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "resource checkpoint complete CAS failed"); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `
UPDATE sessions
SET status = 'checkpointed',
    checkpoint_path = ?,
    updated_at = ?
WHERE id = ?
  AND status = 'checkpointing'
  AND active_generation_id = ?`, p.CheckpointPath, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(res, "session checkpoint complete CAS failed"); err != nil {
		return err
	}
	return tx.Commit()
}
