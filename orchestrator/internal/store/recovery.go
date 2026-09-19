package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ReconcileStaleSessionStates repairs the durable state drift left by an
// interrupted bridge/runtime failure. It is intentionally conservative: only
// running_active sessions whose active generation is terminal or whose tracked
// resource is already absent/destroyed are touched. Healthy active generations
// are left entirely to the heartbeat/lease watchdog.
//
// The entire repair for each session happens in one transaction: in-flight
// leased turns are requeued (they never reached the agent), running turns are
// failed (their agent context may be gone), the generation is fenced failed
// when necessary, and the session status plus a durable generation.error event
// are committed together. This makes the database self-consistent even if the
// process exits immediately afterwards.
func (s *Store) ReconcileStaleSessionStates(ctx context.Context, p SessionRuntimeReconciliationParams) ([]SessionRuntimeReconciliation, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if strings.TrimSpace(p.OwnerUUID) == "" {
		return nil, fmt.Errorf("owner uuid is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, p.OwnerUUID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT s.id, COALESCE(s.active_generation_id, ''),
       COALESCE(g.status, ''), COALESCE(ri.state, '')
FROM sessions s
LEFT JOIN runtime_generations g
  ON g.generation_id = s.active_generation_id
 AND g.session_id = s.id
LEFT JOIN runtime_resource_instances ri
  ON ri.generation_id = g.generation_id
 AND ri.session_id = s.id
WHERE s.status = 'running_active'
  AND NOT EXISTS(SELECT 1 FROM session_execution_backends b WHERE b.session_id=s.id AND b.backend='khora')
  AND (
    g.generation_id IS NULL
    OR g.status IN ('failed', 'destroyed')
    OR ri.generation_id IS NULL
    OR ri.state IN ('destroyed', 'absent_verified')
  )
ORDER BY s.updated_at, s.id`)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		sessionID, generationID, generationStatus, resourceState string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.sessionID, &c.generationID, &c.generationStatus, &c.resourceState); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	now := formatTime(p.Now)
	result := make([]SessionRuntimeReconciliation, 0, len(candidates))
	for _, c := range candidates {
		// Re-check the fence inside the write transaction. A concurrent request
		// may already have installed a replacement generation since the scan.
		var currentGeneration, sessionStatus string
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(active_generation_id, ''), status
FROM sessions WHERE id = ?`, c.sessionID).Scan(&currentGeneration, &sessionStatus); err != nil {
			return nil, err
		}
		if sessionStatus != "running_active" || currentGeneration != c.generationID {
			continue
		}

		// A leased turn has not acknowledged execution and is safe to retry on a
		// replacement generation. A running turn has crossed the start fence; its
		// in-memory agent context is no longer trustworthy, so make the loss
		// explicit instead of replaying it against a dead runtime.
		stateChanged := false
		if c.generationID != "" {
			res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'queued',
    generation_id = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    claim_request_id = NULL,
    claim_granted_at = NULL,
    ack_started_at = NULL,
    attempt = attempt + 1,
    error_class = NULL,
    error = NULL
			WHERE session_id = ?
			  AND generation_id = ?
			  AND status = 'leased'`, c.sessionID, c.generationID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
			res, err = tx.ExecContext(ctx, `
UPDATE turns
SET status = 'failed',
    completed_at = ?,
    completed_by_generation = generation_id,
    lease_owner = NULL,
    lease_expires_at = NULL,
    error_class = 'runtime_reconciled_stall',
    error = 'active runtime is no longer available'
			WHERE session_id = ?
			  AND generation_id = ?
			  AND status = 'running'`, now, c.sessionID, c.generationID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
		} else {
			// A running_active session with no active generation is an impossible
			// but recoverable state. Treat every unowned leased turn as retryable and
			// every running turn as interrupted; there is no runtime fence left to
			// safely resume against.
			res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'queued',
    generation_id = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    claim_request_id = NULL,
    claim_granted_at = NULL,
    ack_started_at = NULL,
    attempt = attempt + 1,
    error_class = NULL,
    error = NULL
WHERE session_id = ? AND status = 'leased'`, c.sessionID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
			res, err = tx.ExecContext(ctx, `
UPDATE turns
SET status = 'failed',
    completed_at = ?,
    completed_by_generation = generation_id,
    lease_owner = NULL,
    lease_expires_at = NULL,
    error_class = 'runtime_reconciled_stall',
    error = 'active runtime is no longer available'
WHERE session_id = ? AND status = 'running'`, now, c.sessionID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
		}

		// If the generation row is still non-terminal, fence it now. Terminal
		// rows are left untouched but still participate in the session repair.
		if c.generationID != "" {
			res, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = 'runtime_reconciled_stall',
    failure_reason = 'active runtime is no longer available',
    ended_at = COALESCE(ended_at, ?),
    lease_owner = NULL,
    lease_expires_at = NULL
			WHERE generation_id = ?
			  AND session_id = ?
			  AND status NOT IN ('failed', 'destroyed')`, now, c.generationID, c.sessionID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
			res, err = tx.ExecContext(ctx, `
UPDATE network_profiles
SET allocation_state = 'reclaimable'
WHERE generation_id = ?
  AND session_id = ?
			  AND allocation_state IN ('allocating','ready','live','reserved_checkpointed','recreating')`, c.generationID, c.sessionID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
			res, err = tx.ExecContext(ctx, `
UPDATE runtime_generation_resources
SET resource_state = 'reclaimable'
WHERE generation_id = ?
  AND resource_state IN ('allocating','ready','live','reserved_checkpointed','recreating')`, c.generationID)
			if err != nil {
				return nil, err
			}
			if affected, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if affected > 0 {
				stateChanged = true
			}
		}

		var nextStatus string
		if err := tx.QueryRowContext(ctx, `
SELECT CASE WHEN EXISTS (
         SELECT 1 FROM turns
         WHERE session_id = ? AND `+pendingTurnPredicate+`
       ) THEN 'running_active' ELSE 'running_idle' END`, c.sessionID).Scan(&nextStatus); err != nil {
			return nil, err
		}
		if c.generationID == "" && nextStatus == "running_active" {
			// No generation exists to cold-fallback from. Let the next user
			// message allocate one and claim any queued turns instead of leaving
			// the session input-blocked forever.
			nextStatus = "running_idle"
		}
		if !stateChanged && nextStatus == sessionStatus {
			continue
		}
		res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = ?,
    updated_at = ?,
    restore_ms = NULL,
    error_class = NULL,
    failure_reason = NULL
WHERE id = ?
  AND status = 'running_active'
  AND ((? <> '' AND active_generation_id = ?)
       OR (? = '' AND active_generation_id IS NULL))`,
			nextStatus, now, c.sessionID, c.generationID, c.generationID, c.generationID)
		if err != nil {
			return nil, err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			continue
		}
		eventID, err := appendEventTx(ctx, tx, AppendEventParams{
			SessionID:    c.sessionID,
			GenerationID: c.generationID,
			DedupeKey:    "runtime_reconciled:" + c.generationID,
			Type:         "generation.error",
			Payload: map[string]any{
				"terminal":             false,
				"error_class":          "runtime_reconciled_stall",
				"error":                "active runtime is no longer available",
				"generation_id":        c.generationID,
				"session_status":       nextStatus,
				"session_updated_at":   now,
				"active_generation_id": c.generationID,
			},
			Now: p.Now,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, SessionRuntimeReconciliation{
			SessionID:     c.sessionID,
			GenerationID:  c.generationID,
			SessionStatus: nextStatus,
			EventID:       eventID,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) ListExpiredRuntimeRecoveryCandidates(ctx context.Context, p StartupRecoveryParams) ([]ExpiredRuntimeRecoveryCandidate, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.AckStartedGrace <= 0 {
		p.AckStartedGrace = p.ReconnectGrace
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, p.OwnerUUID); err != nil {
		return nil, err
	}

	var candidates []ExpiredRuntimeRecoveryCandidate
	rows, err := tx.QueryContext(ctx, `
SELECT s.id, g.generation_id, COALESCE(r.runsc_container_id, ''), g.status,
       EXISTS (SELECT 1 FROM turns t WHERE t.generation_id = g.generation_id AND t.status IN ('leased','running'))
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.status IN ('allocating','starting','probing','restoring','checkpointing')
  AND g.lease_expires_at IS NOT NULL
  AND g.lease_expires_at <= ?
  AND s.active_generation_id = g.generation_id
  AND s.status NOT IN ('failed', 'destroyed')
ORDER BY s.id, g.generation_id`, formatTime(p.Now))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c ExpiredRuntimeRecoveryCandidate
		if err := rows.Scan(&c.SessionID, &c.GenerationID, &c.RuntimeID, &c.Status, &c.InterruptedWork); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.ErrorClass = "orchestrator_restart_during_" + c.Status
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// A live generation whose lease lapsed under the SAME owner was not lost to
	// a restart: this process was here the whole time and its sandbox simply
	// stopped heartbeating (crashed, hung, or was killed). The routing class
	// below keeps its historical "orchestrator_restart_*" name because the
	// repair branches key on it; Cause carries the truthful diagnosis into the
	// generation row and the user-facing event instead.
	ownLease := GenerationLeaseOwner(p.OwnerUUID)
	ackStartedCutoff := p.Now.Add(-p.AckStartedGrace)
	rows, err = tx.QueryContext(ctx, `
SELECT s.id, g.generation_id, COALESCE(r.runsc_container_id, ''), g.status, COALESCE(g.lease_owner, ''),
       EXISTS (SELECT 1 FROM turns t WHERE t.generation_id = g.generation_id AND t.status IN ('leased','running'))
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.status IN ('active','idle')
  AND g.lease_expires_at IS NOT NULL
  AND g.lease_expires_at <= ?
  AND s.active_generation_id = g.generation_id
  AND s.status NOT IN ('failed', 'destroyed')
  AND EXISTS (
    SELECT 1 FROM turns
    WHERE turns.generation_id = g.generation_id
      AND turns.status = 'running'
      AND turns.ack_started_at IS NOT NULL
      AND turns.lease_expires_at IS NOT NULL
      AND turns.lease_expires_at <= ?
  )
ORDER BY s.id, g.generation_id`, formatTime(ackStartedCutoff), formatTime(ackStartedCutoff))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c ExpiredRuntimeRecoveryCandidate
		var leaseOwner string
		if err := rows.Scan(&c.SessionID, &c.GenerationID, &c.RuntimeID, &c.Status, &leaseOwner, &c.InterruptedWork); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.ErrorClass = "unknown_after_ack_started"
		if leaseOwner == ownLease {
			c.Cause = SandboxHeartbeatLostCause
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	cutoff := p.Now.Add(-p.ReconnectGrace)
	rows, err = tx.QueryContext(ctx, `
SELECT s.id, g.generation_id, COALESCE(r.runsc_container_id, ''), g.status, COALESCE(g.lease_owner, ''),
       EXISTS (SELECT 1 FROM turns t WHERE t.generation_id = g.generation_id AND t.status IN ('leased','running'))
FROM runtime_generations g
JOIN sessions s ON s.id = g.session_id
JOIN runtime_generation_resources r ON r.generation_id = g.generation_id
WHERE g.status IN ('active','idle')
  AND g.lease_expires_at IS NOT NULL
  AND g.lease_expires_at <= ?
  AND s.active_generation_id = g.generation_id
  AND s.status NOT IN ('failed', 'destroyed')
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE turns.generation_id = g.generation_id
      AND turns.status = 'running'
      AND turns.ack_started_at IS NOT NULL
  )
ORDER BY s.id, g.generation_id`, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c ExpiredRuntimeRecoveryCandidate
		var leaseOwner string
		if err := rows.Scan(&c.SessionID, &c.GenerationID, &c.RuntimeID, &c.Status, &leaseOwner, &c.InterruptedWork); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.ErrorClass = "orchestrator_restart_reconnect_grace_expired"
		if leaseOwner == ownLease {
			c.Cause = SandboxHeartbeatLostCause
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Store) RepairExpiredRuntimeRecovery(ctx context.Context, p StartupRecoveryParams, candidates []ExpiredRuntimeRecoveryCandidate) (StartupRecoveryResult, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.AckStartedGrace <= 0 {
		p.AckStartedGrace = p.ReconnectGrace
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StartupRecoveryResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := assertOwnerTx(ctx, tx, p.OwnerUUID); err != nil {
		return StartupRecoveryResult{}, err
	}
	result := StartupRecoveryResult{}
	now := formatTime(p.Now)
	lifecycleIDs, unknownIDs, reconnectIDs := recoveryCandidateIDs(candidates)

	lifecycleIDs, err = filterLifecycleRecoveryIDsTx(ctx, tx, lifecycleIDs, p.Now)
	if err != nil {
		return StartupRecoveryResult{}, err
	}
	if len(lifecycleIDs) > 0 {
		requeued, err := requeueExpiredLeasedTurnsTx(ctx, tx, lifecycleIDs, p.Now)
		if err != nil {
			return StartupRecoveryResult{}, err
		}
		result.ExpiredLeasedRequeued += requeued
		args := []any{now}
		args = appendStringIDs(args, lifecycleIDs)
		if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = 'orchestrator_restart_during_' || status,
    failure_reason = 'orchestrator_restart_during_' || status,
    ended_at = ?,
    lease_owner = NULL
WHERE generation_id IN (`+sqlPlaceholders(len(lifecycleIDs))+`)`, args...); err != nil {
			return StartupRecoveryResult{}, err
		}
		if err := markAllocationsReclaimableTx(ctx, tx, lifecycleIDs); err != nil {
			return StartupRecoveryResult{}, err
		}
		if err := deleteActiveContextsForGenerationsTx(ctx, tx, lifecycleIDs); err != nil {
			return StartupRecoveryResult{}, err
		}
		result.ExpiredLifecycleFailed = int64(len(lifecycleIDs))
	}

	unknownIDs, err = filterUnknownRecoveryIDsTx(ctx, tx, unknownIDs, p.Now.Add(-p.AckStartedGrace))
	if err != nil {
		return StartupRecoveryResult{}, err
	}
	if len(unknownIDs) > 0 {
		args := []any{now}
		args = appendStringIDs(args, unknownIDs)
		res, err := tx.ExecContext(ctx, `
UPDATE turns
SET status = 'failed',
    completed_at = ?,
    completed_by_generation = generation_id,
    error_class = 'unknown_after_ack_started',
    error = 'unknown_after_ack_started',
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE generation_id IN (`+sqlPlaceholders(len(unknownIDs))+`)
  AND status = 'running'
  AND ack_started_at IS NOT NULL`, args...)
		if err != nil {
			return StartupRecoveryResult{}, err
		}
		unknownTurns, err := res.RowsAffected()
		if err != nil {
			return StartupRecoveryResult{}, err
		}
		args = []any{now}
		args = appendStringIDs(args, unknownIDs)
		if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = 'unknown_after_ack_started',
    failure_reason = 'unknown_after_ack_started',
    ended_at = ?,
    lease_owner = NULL
WHERE generation_id IN (`+sqlPlaceholders(len(unknownIDs))+`)`, args...); err != nil {
			return StartupRecoveryResult{}, err
		}
		if err := markAllocationsReclaimableTx(ctx, tx, unknownIDs); err != nil {
			return StartupRecoveryResult{}, err
		}
		if err := deleteActiveContextsForGenerationsTx(ctx, tx, unknownIDs); err != nil {
			return StartupRecoveryResult{}, err
		}
		result.UnknownAfterAckStarted += unknownTurns
	}

	reconnectIDs, err = filterReconnectRecoveryIDsTx(ctx, tx, reconnectIDs, p.Now.Add(-p.ReconnectGrace))
	if err != nil {
		return StartupRecoveryResult{}, err
	}
	if len(reconnectIDs) > 0 {
		requeued, err := requeueExpiredLeasedTurnsTx(ctx, tx, reconnectIDs, p.Now)
		if err != nil {
			return StartupRecoveryResult{}, err
		}
		result.ExpiredLeasedRequeued += requeued
		args := []any{now}
		args = appendStringIDs(args, reconnectIDs)
		if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'failed',
    error_class = 'orchestrator_restart_reconnect_grace_expired',
    failure_reason = 'orchestrator_restart_reconnect_grace_expired',
    ended_at = ?,
    lease_owner = NULL
WHERE generation_id IN (`+sqlPlaceholders(len(reconnectIDs))+`)`, args...); err != nil {
			return StartupRecoveryResult{}, err
		}
		if err := markAllocationsReclaimableTx(ctx, tx, reconnectIDs); err != nil {
			return StartupRecoveryResult{}, err
		}
		if err := deleteActiveContextsForGenerationsTx(ctx, tx, reconnectIDs); err != nil {
			return StartupRecoveryResult{}, err
		}
		result.ReconnectGraceFailed = int64(len(reconnectIDs))
	}
	repairedIDs := append(append([]string{}, lifecycleIDs...), unknownIDs...)
	repairedIDs = append(repairedIDs, reconnectIDs...)
	// Overwrite the routing-derived error class with the diagnosed cause where we
	// have one. The branches above record the class that selected them
	// ("unknown_after_ack_started" and friends), which describes how recovery
	// found the generation rather than why it died. When the memcg says the
	// workload was OOM-killed, the generation row should say so: it is the only
	// durable record of the cause, and support reads it long after the transcript
	// notice scrolls away.
	//
	// Applied after the branches, and only to generations they actually repaired,
	// so it can never route a candidate to a different branch (see the Cause field
	// comment on ExpiredRuntimeRecoveryCandidate) — but BEFORE the session repair
	// below, whose generation.error event copies error_class/failure_reason into
	// the payload the UI shows. Writing the cause after that event was how users
	// came to see "orchestrator_restart_reconnect_grace_expired" as a toast for a
	// sandbox that died while the orchestrator never restarted.
	repairedSet := make(map[string]bool, len(repairedIDs))
	for _, id := range repairedIDs {
		repairedSet[id] = true
	}
	for _, candidate := range candidates {
		if candidate.Cause == "" || !repairedSet[candidate.GenerationID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE runtime_generations
SET error_class = ?,
    failure_reason = ?
WHERE generation_id = ?`, candidate.Cause, candidate.Cause, candidate.GenerationID); err != nil {
			return StartupRecoveryResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE turns
SET error_class = ?,
    error = ?
WHERE generation_id = ?
  AND status = 'failed'
  AND completed_by_generation = generation_id`, candidate.Cause, candidate.Cause, candidate.GenerationID); err != nil {
			return StartupRecoveryResult{}, err
		}
	}
	for _, generationID := range repairedIDs {
		eventID, err := repairRecoveredSessionTx(ctx, tx, generationID, p.Now)
		if err != nil {
			return StartupRecoveryResult{}, err
		}
		if eventID != 0 {
			result.EventIDs = append(result.EventIDs, eventID)
		}
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM active_model_request_contexts
WHERE lease_owner NOT LIKE ?`, p.OwnerUUID+":%"); err != nil {
		return StartupRecoveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StartupRecoveryResult{}, err
	}
	return result, nil
}

func recoveryCandidateIDs(candidates []ExpiredRuntimeRecoveryCandidate) (lifecycleIDs, unknownIDs, reconnectIDs []string) {
	for _, candidate := range candidates {
		switch candidate.ErrorClass {
		case "unknown_after_ack_started":
			unknownIDs = append(unknownIDs, candidate.GenerationID)
		case "orchestrator_restart_reconnect_grace_expired":
			reconnectIDs = append(reconnectIDs, candidate.GenerationID)
		default:
			if strings.HasPrefix(candidate.ErrorClass, "orchestrator_restart_during_") {
				lifecycleIDs = append(lifecycleIDs, candidate.GenerationID)
			}
		}
	}
	return lifecycleIDs, unknownIDs, reconnectIDs
}

func filterLifecycleRecoveryIDsTx(ctx context.Context, tx *sql.Tx, ids []string, now time.Time) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := appendStringIDs([]any{formatTime(now)}, ids)
	return queryStringColumnTx(ctx, tx, `
SELECT generation_id
FROM runtime_generations
WHERE status IN ('allocating','starting','probing','restoring','checkpointing')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE sessions.id = runtime_generations.session_id
      AND sessions.active_generation_id = runtime_generations.generation_id
      AND sessions.status NOT IN ('failed', 'destroyed')
  )
  AND generation_id IN (`+sqlPlaceholders(len(ids))+`)`, args...)
}

func filterUnknownRecoveryIDsTx(ctx context.Context, tx *sql.Tx, ids []string, cutoff time.Time) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := appendStringIDs([]any{formatTime(cutoff), formatTime(cutoff)}, ids)
	return queryStringColumnTx(ctx, tx, `
SELECT generation_id
FROM runtime_generations
WHERE status IN ('active','idle')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE sessions.id = runtime_generations.session_id
      AND sessions.active_generation_id = runtime_generations.generation_id
      AND sessions.status NOT IN ('failed', 'destroyed')
  )
  AND EXISTS (
    SELECT 1 FROM turns
    WHERE turns.generation_id = runtime_generations.generation_id
      AND turns.status = 'running'
      AND turns.ack_started_at IS NOT NULL
      AND turns.lease_expires_at IS NOT NULL
      AND turns.lease_expires_at <= ?
  )
  AND generation_id IN (`+sqlPlaceholders(len(ids))+`)`, args...)
}

func filterReconnectRecoveryIDsTx(ctx context.Context, tx *sql.Tx, ids []string, cutoff time.Time) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := appendStringIDs([]any{formatTime(cutoff)}, ids)
	return queryStringColumnTx(ctx, tx, `
SELECT generation_id
FROM runtime_generations
WHERE status IN ('active','idle')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= ?
  AND EXISTS (
    SELECT 1 FROM sessions
    WHERE sessions.id = runtime_generations.session_id
      AND sessions.active_generation_id = runtime_generations.generation_id
      AND sessions.status NOT IN ('failed', 'destroyed')
  )
  AND NOT EXISTS (
    SELECT 1 FROM turns
    WHERE turns.generation_id = runtime_generations.generation_id
      AND turns.status = 'running'
      AND turns.ack_started_at IS NOT NULL
  )
  AND generation_id IN (`+sqlPlaceholders(len(ids))+`)`, args...)
}

func repairRecoveredSessionTx(ctx context.Context, tx *sql.Tx, generationID string, now time.Time) (int64, error) {
	nowString := formatTime(now)
	res, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status = CASE
      WHEN EXISTS (
        SELECT 1 FROM turns
        WHERE turns.session_id = sessions.id
          AND `+pendingTurnPredicateFor("turns")+`
      ) THEN 'running_active'
      ELSE 'running_idle'
    END,
    checkpoint_path = NULL,
    restore_ms = NULL,
    error_class = NULL,
    failure_reason = NULL,
    updated_at = ?
WHERE active_generation_id = ?
  AND status NOT IN ('failed', 'destroyed')`, nowString, generationID)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, nil
	}
	var sessionID, sessionStatus, errorClass, reason string
	if err := tx.QueryRowContext(ctx, `
SELECT s.id, s.status, COALESCE(g.error_class, ''), COALESCE(g.failure_reason, '')
FROM sessions s
JOIN runtime_generations g ON g.generation_id = s.active_generation_id
WHERE s.active_generation_id = ?`, generationID).Scan(&sessionID, &sessionStatus, &errorClass, &reason); err != nil {
		return 0, err
	}
	return appendEventTx(ctx, tx, AppendEventParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		DedupeKey:    "runtime_recovery:" + generationID + ":" + errorClass,
		Type:         "generation.error",
		Payload: map[string]any{
			"terminal":             false,
			"error_class":          errorClass,
			"error":                reason,
			"generation_id":        generationID,
			"session_status":       sessionStatus,
			"session_updated_at":   nowString,
			"active_generation_id": generationID,
			"restore_ms":           nil,
		},
		Now: now,
	})
}
