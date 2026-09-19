package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

// PinBackendIdentity refuses to reinterpret old opaque IDs after an engine
// rebuild. The old binding remains available for explicit recovery/migration.
func (s *Store) PinBackendIdentity(ctx context.Context, identity execution.BackendIdentity) error {
	if identity.Name == "" || identity.StoreID == "" || identity.Version == "" {
		return fmt.Errorf("backend name, durable store identity and version required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO sandbox_backends(name,store_id,version) VALUES(?,?,?)`, identity.Name, identity.StoreID, identity.Version)
	if err != nil {
		return err
	}
	var pinned string
	if err := tx.QueryRowContext(ctx, `SELECT store_id FROM sandbox_backends WHERE name=?`, identity.Name).Scan(&pinned); err != nil {
		return err
	}
	if pinned != identity.StoreID {
		return execution.ErrBackendIdentityChanged
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sandbox_backends SET version=? WHERE name=? AND store_id=?`, identity.Version, identity.Name, identity.StoreID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateEnvironmentBinding(ctx context.Context, p execution.Environment) error {
	if p.ID == "" || p.SessionID == "" || p.BackendName == "" || p.StoreID == "" || p.SandboxID != "" || p.Fence != 0 || p.Epoch != 0 || p.MutationRevision != "" || p.ObservedState != "" {
		return fmt.Errorf("new environment requires session/backend identity and no observed execution")
	}
	desired, err := normalizedOperationJSON(p.Desired)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var identity string
	var backend string
	if err := tx.QueryRowContext(ctx, `SELECT backend FROM session_execution_backends WHERE session_id=?`, p.SessionID).Scan(&backend); err != nil {
		return err
	}
	if backend != "khora" {
		return fmt.Errorf("session is pinned to another execution backend")
	}
	if err := tx.QueryRowContext(ctx, `SELECT store_id FROM sandbox_backends WHERE name=?`, p.BackendName).Scan(&identity); err != nil {
		return err
	}
	if identity != p.StoreID {
		return execution.ErrBackendIdentityChanged
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO sandbox_environments(environment_id,session_id,backend_name,store_id,desired,fence) VALUES(?,?,?,?,?,0)`, p.ID, p.SessionID, p.BackendName, p.StoreID, string(desired))
	if err != nil {
		return err
	}
	stored, err := environmentWith(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if stored.SessionID != p.SessionID || stored.BackendName != p.BackendName || stored.StoreID != p.StoreID || !bytes.Equal(stored.Desired, desired) {
		return execution.ErrOperationConflict
	}
	return tx.Commit()
}

func (s *Store) GetEnvironmentBinding(ctx context.Context, id string) (execution.Environment, error) {
	return environmentWith(ctx, s.db, id)
}
func environmentWith(ctx context.Context, db dbRunner, id string) (execution.Environment, error) {
	var p execution.Environment
	var desired string
	err := db.QueryRowContext(ctx, `SELECT environment_id,session_id,backend_name,store_id,coalesce(sandbox_id,''),desired,fence,mutation_revision,epoch,observed_state FROM sandbox_environments WHERE environment_id=?`, id).Scan(&p.ID, &p.SessionID, &p.BackendName, &p.StoreID, &p.SandboxID, &desired, &p.Fence, &p.MutationRevision, &p.Epoch, &p.ObservedState)
	p.Desired = json.RawMessage(desired)
	return p, err
}

func (s *Store) BeginEnvironmentOperation(ctx context.Context, p execution.OperationIntent) (execution.Operation, error) {
	if p.ID == "" || p.EnvironmentID == "" || p.IdempotencyKey == "" || p.ExpectedFence < 0 {
		return execution.Operation{}, fmt.Errorf("operation identity, idempotency key and fence required")
	}
	switch p.Kind {
	case "create", "pause", "resume", "configure", "start_process", "workspace_write", "create_volume", "recover_journal", "restore_native":
	default:
		return execution.Operation{}, fmt.Errorf("unsupported environment operation %q", p.Kind)
	}
	request, err := normalizedOperationJSON(p.Request)
	if err != nil {
		return execution.Operation{}, err
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Operation{}, err
	}
	defer tx.Rollback()
	operation, err := beginEnvironmentOperationTx(ctx, tx, p, request)
	if err != nil {
		return execution.Operation{}, err
	}
	return operation, tx.Commit()
}

func beginEnvironmentOperationTx(ctx context.Context, tx *sql.Tx, p execution.OperationIntent, request []byte) (execution.Operation, error) {
	existing, err := operationWith(ctx, tx, p.ID)
	if err == nil {
		if existing.EnvironmentID != p.EnvironmentID || existing.Kind != p.Kind || existing.IdempotencyKey != p.IdempotencyKey || existing.Fence != p.ExpectedFence+1 || !bytes.Equal(existing.Request, request) {
			return execution.Operation{}, execution.ErrOperationConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return execution.Operation{}, err
	}
	environment, err := environmentWith(ctx, tx, p.EnvironmentID)
	if err != nil {
		return execution.Operation{}, err
	}
	if environment.Fence != p.ExpectedFence {
		return execution.Operation{}, execution.ErrStaleOperation
	}
	var readers int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_read_leases WHERE environment_id=?`, p.EnvironmentID).Scan(&readers); err != nil {
		return execution.Operation{}, err
	}
	if readers != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_mutations m WHERE session_id=? AND state IN ('active','uncertain') AND NOT EXISTS(SELECT 1 FROM workspace_write_maintenance w WHERE w.mutation_id=m.operation_id AND w.operation_id=? AND w.stage IN ('resuming','pausing'))`, environment.SessionID, p.ID).Scan(&readers); err != nil {
		return execution.Operation{}, err
	}
	if readers != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	var recovering int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_recoveries WHERE environment_id=? AND stage!='complete' AND operation_id!=?`, p.EnvironmentID, p.ID).Scan(&recovering); err != nil {
		return execution.Operation{}, err
	}
	if recovering != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM native_capture_activations WHERE environment_id=? AND stage IN ('resuming','reading','pausing') AND operation_id!=?`, p.EnvironmentID, p.ID).Scan(&recovering); err != nil {
		return execution.Operation{}, err
	}
	if recovering != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	if p.Kind == "start_process" || p.Kind == "recover_journal" {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired'`, p.EnvironmentID).Scan(&active); err != nil {
			return execution.Operation{}, err
		}
		if active != 0 {
			return execution.Operation{}, execution.ErrBusy
		}
	}

	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_operations WHERE environment_id=? AND state IN ('pending','issued','uncertain')`, p.EnvironmentID).Scan(&unresolved); err != nil {
		return execution.Operation{}, err
	}
	if unresolved != 0 {
		return execution.Operation{}, execution.ErrOperationConflict
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_operations WHERE environment_id=? AND idempotency_key=?`, p.EnvironmentID, p.IdempotencyKey).Scan(&used); err != nil {
		return execution.Operation{}, err
	}
	if used != 0 {
		return execution.Operation{}, execution.ErrOperationConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE sandbox_environments SET fence=fence+1 WHERE environment_id=? AND fence=?`, p.EnvironmentID, p.ExpectedFence)
	if err != nil {
		return execution.Operation{}, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return execution.Operation{}, execution.ErrStaleOperation
	}
	hash := sha256.Sum256(request)
	_, err = tx.ExecContext(ctx, `INSERT INTO environment_operations(operation_id,environment_id,kind,idempotency_key,fence,mutation_revision,state,request,request_digest,result,error,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?,'{}','',?,?)`, p.ID, p.EnvironmentID, p.Kind, p.IdempotencyKey, p.ExpectedFence+1, environment.MutationRevision, string(request), hex.EncodeToString(hash[:]), formatTime(p.Now), formatTime(p.Now))
	if err != nil {
		return execution.Operation{}, err
	}
	operation, err := operationWith(ctx, tx, p.ID)
	if err != nil {
		return execution.Operation{}, err
	}
	return operation, nil
}

func (s *Store) GetEnvironmentOperation(ctx context.Context, id string) (execution.Operation, error) {
	return operationWith(ctx, s.db, id)
}
func operationWith(ctx context.Context, db dbRunner, id string) (execution.Operation, error) {
	var p execution.Operation
	var request, result, created, updated, digest string
	err := db.QueryRowContext(ctx, `SELECT operation_id,environment_id,kind,idempotency_key,fence,mutation_revision,state,request,result,error,created_at,updated_at,request_digest FROM environment_operations WHERE operation_id=?`, id).Scan(&p.ID, &p.EnvironmentID, &p.Kind, &p.IdempotencyKey, &p.Fence, &p.MutationRevision, &p.State, &request, &result, &p.Error, &created, &updated, &digest)
	if err == nil {
		hash := sha256.Sum256([]byte(request))
		if hex.EncodeToString(hash[:]) != digest {
			return execution.Operation{}, execution.ErrOperationConflict
		}
	}
	p.Request = json.RawMessage(request)
	p.Result = json.RawMessage(result)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, err
}

func (s *Store) RecordEnvironmentOperationOutcome(ctx context.Context, p execution.OperationOutcome) error {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.ID == "" || p.Fence <= 0 {
		return execution.ErrStaleOperation
	}
	result, err := normalizedOperationJSON(p.Result)
	if err != nil {
		return err
	}
	switch p.State {
	case execution.OperationIssued, execution.OperationUncertain, execution.OperationSucceeded, execution.OperationFailed:
	default:
		return fmt.Errorf("invalid environment operation outcome")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operation, err := operationWith(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	environment, err := environmentWith(ctx, tx, operation.EnvironmentID)
	if err != nil {
		return err
	}
	if operation.Fence != p.Fence || environment.Fence != p.Fence {
		return execution.ErrStaleOperation
	}
	if p.State == execution.OperationFailed && strings.TrimSpace(p.Error) == "" {
		return fmt.Errorf("terminal operation failure requires a verified reason")
	}
	if p.State == execution.OperationSucceeded {
		switch operation.Kind {
		case "restore_native":
			if !bytes.Equal(operation.Request, result) {
				return execution.ErrOperationConflict
			}
			var request execution.NativeRestoreRequest
			if err := json.Unmarshal(operation.Request, &request); err != nil {
				return err
			}
			state, err := getDriverStateTx(ctx, tx, environment.SessionID, "pi")
			if err != nil {
				return err
			}
			if state.StateDigest != request.StateDigest || state.StateVersion != request.StateVersion || request.Reference.StoreID != environment.StoreID || request.Reference.SandboxID != environment.SandboxID || request.Reference.Epoch != environment.Epoch || request.Reference.MutationRevision != environment.MutationRevision {
				return execution.ErrStaleOperation
			}
		case "recover_journal":
			var request execution.JournalRecoveryRequest
			var recorded, completed execution.JournalRecoveryResult
			if err := json.Unmarshal(operation.Request, &request); err != nil {
				return err
			}
			if err := json.Unmarshal(operation.Result, &recorded); err != nil {
				return err
			}
			if err := json.Unmarshal(result, &completed); err != nil {
				return err
			}
			if recorded.Head == nil || completed.Head == nil || *recorded.Head != *completed.Head || !completed.ReaderStopped {
				return fmt.Errorf("journal recovery requires captured head and stopped reader")
			}
			var watermark uint64
			if err := tx.QueryRowContext(ctx, `SELECT watermark FROM bridge_streams WHERE backend_id=? AND generation_id=?`, environment.StoreID, request.GenerationID).Scan(&watermark); err != nil {
				return err
			}
			if watermark != *completed.Head {
				return fmt.Errorf("journal recovery has uncommitted records")
			}
		case "create", "pause", "resume", "configure":
			if p.SandboxID == "" || p.MutationRevision == "" || p.Epoch == 0 {
				return fmt.Errorf("successful lifecycle operation requires a complete current observation")
			}
		}
	}
	if operation.State == execution.OperationSucceeded || operation.State == execution.OperationFailed {
		if operation.State != p.State || !bytes.Equal(operation.Result, result) || operation.Error != p.Error {
			return execution.ErrOperationConflict
		}
		if (p.SandboxID != "" || p.MutationRevision != "" || p.Epoch != 0) && (p.SandboxID != environment.SandboxID || p.MutationRevision != environment.MutationRevision || p.Epoch != environment.Epoch) {
			return execution.ErrOperationConflict
		}
		return tx.Commit()
	}
	if p.State == execution.OperationIssued && operation.State != execution.OperationPending {
		return execution.ErrOperationConflict
	}
	if p.State != execution.OperationSucceeded && (p.SandboxID != "" || p.MutationRevision != "" || p.Epoch != 0) {
		return fmt.Errorf("only a reconciled successful operation can update environment observation")
	}
	if p.State == execution.OperationSucceeded && (p.SandboxID != "" || p.MutationRevision != "" || p.Epoch != 0) {
		var observedState string
		switch operation.Kind {
		case "create", "resume":
			observedState = "running"
		case "pause", "configure":
			observedState = "paused"
		default:
			return fmt.Errorf("only lifecycle operations can record environment observations")
		}
		if p.SandboxID == "" || p.MutationRevision == "" || p.Epoch == 0 || p.Epoch < environment.Epoch {
			return fmt.Errorf("complete current sandbox observation required")
		}
		if environment.SandboxID != "" && environment.SandboxID != p.SandboxID {
			return execution.ErrOperationConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sandbox_environments SET sandbox_id=?,mutation_revision=?,epoch=?,observed_state=? WHERE environment_id=? AND fence=?`, p.SandboxID, p.MutationRevision, p.Epoch, observedState, environment.ID, p.Fence); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE environment_operations SET state=?,result=?,error=?,updated_at=? WHERE operation_id=? AND fence=?`, p.State, string(result), p.Error, formatTime(p.Now), p.ID, p.Fence)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func normalizedOperationJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid operation JSON")
	}
	return json.Marshal(value)
}

func (s *Store) GetSessionEnvironment(ctx context.Context, sessionID string) (execution.Environment, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT environment_id FROM sandbox_environments WHERE session_id=?`, sessionID).Scan(&id); err != nil {
		return execution.Environment{}, err
	}
	return s.GetEnvironmentBinding(ctx, id)
}

func (s *Store) ListUnresolvedEnvironmentOperations(ctx context.Context, limit int) ([]execution.Operation, error) {
	if limit <= 0 || limit > 1024 {
		limit = 128
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id FROM environment_operations WHERE state IN ('pending','issued','uncertain') ORDER BY created_at,operation_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var result []execution.Operation
	for _, id := range ids {
		operation, err := s.GetEnvironmentOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		if operation.State == execution.OperationPending || operation.State == execution.OperationIssued || operation.State == execution.OperationUncertain {
			result = append(result, operation)
		}
	}
	return result, nil
}

func (s *Store) FindEnvironmentOperation(ctx context.Context, id string) (execution.Operation, bool, error) {
	operation, err := s.GetEnvironmentOperation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return operation, false, nil
	}
	return operation, err == nil, err
}
