package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"net/url"
	"time"

	"harness-platform/orchestrator/internal/health"
)

type QueueDiagnostic struct {
	Count      int64   `json:"count"`
	OldestAt   string  `json:"oldest_at,omitempty"`
	AgeSeconds float64 `json:"oldest_age_seconds"`
}
type PendingOperationDiagnostic struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	State      string  `json:"state"`
	Error      string  `json:"error,omitempty"`
	AgeSeconds float64 `json:"age_seconds"`
}
type TuplePoolDiagnostic struct {
	StoreID   string `json:"store_id"`
	Range     string `json:"range"`
	Capacity  uint64 `json:"capacity"`
	Reserved  uint64 `json:"reserved"`
	Available uint64 `json:"available"`
}
type PipelineFailure struct {
	Pipeline   string `json:"pipeline"`
	SessionID  string `json:"session_id"`
	Error      string `json:"error"`
	RetryAfter string `json:"retry_after"`
}
type OperationalSnapshot struct {
	Failures         []PipelineFailure            `json:"pipeline_failures"`
	At               time.Time                    `json:"at"`
	OwnerHeartbeatAt string                       `json:"owner_heartbeat_at,omitempty"`
	Workers          []health.Status              `json:"workers"`
	Queues           map[string]QueueDiagnostic   `json:"queues"`
	Storage          map[string]int64             `json:"storage_bytes"`
	Capacity         map[string]int64             `json:"capacity"`
	TuplePools       []TuplePoolDiagnostic        `json:"tuple_pools"`
	Operations       []PendingOperationDiagnostic `json:"pending_operations"`
}

// OpenReadOnly validates the current schema without taking writer authority.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := verifySchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) OperationalSnapshot(ctx context.Context, now time.Time) (OperationalSnapshot, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := OperationalSnapshot{At: now, Queues: map[string]QueueDiagnostic{}, Storage: map[string]int64{}, Capacity: map[string]int64{}, Workers: []health.Status{}, TuplePools: []TuplePoolDiagnostic{}, Operations: []PendingOperationDiagnostic{}}
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var currentOwner string
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT heartbeat_at FROM orchestrator_owner WHERE singleton=1),''),coalesce((SELECT uuid FROM orchestrator_owner WHERE singleton=1),'')`).Scan(&result.OwnerHeartbeatAt, &currentOwner); err != nil {
		return result, err
	}
	queues := map[string]string{
		"operations":         `SELECT count(*),min(created_at) FROM environment_operations WHERE state IN ('pending','issued','uncertain')`,
		"foreground_turns":   `SELECT count(*),min(created_at) FROM turns WHERE status='queued' AND auto_dispatch=1 AND materializes_message=1`,
		"continuation_turns": `SELECT count(*),min(created_at) FROM turns WHERE status='queued' AND auto_dispatch=1 AND materializes_message=0`,
		"parked_turns":       `SELECT count(*),min(created_at) FROM turns WHERE status='queued' AND auto_dispatch=0`,
		"scheduled_runs":     `SELECT count(*),min(created_at) FROM schedule_runs WHERE status='queued'`,
		"gateway_drains":     `SELECT count(*),min(created_at) FROM egress_grant_drains WHERE state='pending'`,
		"projection_streams": `SELECT count(*),min((SELECT i.created_at FROM bridge_ingress i WHERE i.generation_id=p.generation_id AND i.turn_id=p.turn_id AND i.attempt=p.attempt AND i.id>p.last_record_id ORDER BY i.id LIMIT 1)) FROM bridge_projections p WHERE last_record_id<last_source_record_id`,
		"native_capture":     `SELECT count(*),min(r.created_at) FROM native_capture_requests r WHERE NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=r.session_id AND c.state_version=r.state_version)`,
		"workspace_writes":   `SELECT count(*),min(created_at) FROM workspace_mutations WHERE state IN ('active','uncertain')`,
		"retirements":        `SELECT count(*),min(created_at) FROM session_retirements WHERE state='requested'`,
	}
	for name, query := range queues {
		var row QueueDiagnostic
		var oldest sql.NullString
		if err := tx.QueryRowContext(ctx, query).Scan(&row.Count, &oldest); err != nil {
			return result, fmt.Errorf("diagnose %s: %w", name, err)
		}
		if oldest.Valid {
			row.OldestAt = oldest.String
			row.AgeSeconds = max(0, now.Sub(parseTime(oldest.String)).Seconds())
		}
		result.Queues[name] = row
	}
	for name, query := range map[string]string{
		"native_snapshots_logical":  `SELECT coalesce(sum(size),0) FROM native_capture_requests`,
		"native_archives_logical":   `SELECT coalesce(sum(r.size),0) FROM native_capture_requests r JOIN native_capture_receipts c ON c.session_id=r.session_id AND c.state_version=r.state_version`,
		"workspace_inventory":       `SELECT coalesce(sum(size),0) FROM artifacts`,
		"retained_overlay_capacity": `SELECT coalesce(sum(json_extract(desired,'$.Disk')),0) FROM sandbox_environments`,
		"session_volume_capacity":   `SELECT coalesce(sum(capacity),0) FROM session_storage_bindings`,
	} {
		var value int64
		if err := tx.QueryRowContext(ctx, query).Scan(&value); err != nil {
			return result, err
		}
		result.Storage[name] = value
	}
	for name, query := range map[string]string{
		"running_placements":    `SELECT count(*) FROM sandbox_environments WHERE observed_state='running'`,
		"retained_environments": `SELECT count(*) FROM sandbox_environments`,
		"starting_executions":   `SELECT count(*) FROM execution_bindings WHERE state='starting'`,
		"detached_executions":   `SELECT count(*) FROM execution_bindings WHERE state='accepted' AND detached=1`,
		"running_turns":         `SELECT count(*) FROM turns WHERE status IN ('leased','running')`,
		"capture_activations":   `SELECT count(*) FROM native_capture_activations WHERE stage IN ('resuming','reading','pausing')`,
	} {
		var value int64
		if err := tx.QueryRowContext(ctx, query).Scan(&value); err != nil {
			return result, err
		}
		result.Capacity[name] = value
	}
	occupied, err := countSandboxOccupyingSessions(ctx, tx)
	if err != nil {
		return result, err
	}
	result.Capacity["occupying_sessions"] = int64(occupied)
	var pages, pageSize int64
	if err := tx.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return result, err
	}
	result.Storage["product_database_allocated"] = pages * pageSize
	rows, err := tx.QueryContext(ctx, `SELECT name,state,observed_at,last_success_at,error,stale_after_ns,owner FROM worker_health ORDER BY name`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var status health.Status
		var observed, success, owner string
		if err := rows.Scan(&status.Name, &status.State, &observed, &success, &status.Error, &status.StaleAfter, &owner); err != nil {
			rows.Close()
			return result, err
		}
		status.ObservedAt = parseTime(observed)
		status.LastSuccessAt = parseTime(success)
		status.Stale = now.Sub(status.ObservedAt) > status.StaleAfter || (owner != "" && owner != currentOwner)
		result.Workers = append(result.Workers, status)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT store_id,synthetic_range,next_offset FROM egress_tuple_pools ORDER BY store_id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var pool TuplePoolDiagnostic
		var next uint64
		if err := rows.Scan(&pool.StoreID, &pool.Range, &next); err != nil {
			rows.Close()
			return result, err
		}
		prefix, err := netip.ParsePrefix(pool.Range)
		if err != nil || !prefix.Addr().Is4() {
			rows.Close()
			return result, fmt.Errorf("invalid reserved tuple pool")
		}
		pool.Capacity = (uint64(1) << (32 - prefix.Bits())) - 2
		pool.Reserved = min(next-1, pool.Capacity)
		pool.Available = pool.Capacity - pool.Reserved
		result.TuplePools = append(result.TuplePools, pool)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT operation_id,kind,state,error,created_at FROM environment_operations WHERE state IN ('pending','issued','uncertain') ORDER BY created_at,operation_id LIMIT 32`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var operation PendingOperationDiagnostic
		var created string
		if err := rows.Scan(&operation.ID, &operation.Kind, &operation.State, &operation.Error, &created); err != nil {
			rows.Close()
			return result, err
		}
		operation.AgeSeconds = max(0, now.Sub(parseTime(created)).Seconds())
		result.Operations = append(result.Operations, operation)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT 'native_capture',r.session_id,r.last_error,r.retry_after FROM native_capture_requests r WHERE r.last_error!='' AND NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=r.session_id AND c.state_version=r.state_version)
 UNION ALL SELECT 'projection',t.session_id,p.last_error,p.retry_after FROM bridge_projections p JOIN turns t ON t.id=p.turn_id WHERE p.last_record_id<p.last_source_record_id AND p.last_error!='' LIMIT 32`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var failure PipelineFailure
		if err := rows.Scan(&failure.Pipeline, &failure.SessionID, &failure.Error, &failure.RetryAfter); err != nil {
			rows.Close()
			return result, err
		}
		result.Failures = append(result.Failures, failure)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
