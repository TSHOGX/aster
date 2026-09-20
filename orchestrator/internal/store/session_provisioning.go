package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/canonicaljson"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
)

func (s *Store) AssertExecutionOwner(ctx context.Context, owner string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ExecutionSession(ctx context.Context, id string) (execution.Session, error) {
	var p execution.Session
	err := s.db.QueryRowContext(ctx, `SELECT s.id,s.driver_id,coalesce(s.active_generation_id,'') FROM sessions s WHERE s.id=? AND s.status NOT IN ('failed','destroyed')`, id).Scan(&p.ID, &p.Driver, &p.ActiveGenerationID)
	return p, err
}

func (s *Store) BeginSessionProvisioning(ctx context.Context, intent execution.OperationIntent, owner string, maximum int) (execution.Operation, error) {
	if intent.Kind != "create" {
		return execution.Operation{}, execution.ErrOperationConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Operation{}, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return execution.Operation{}, err
	}
	if _, err := operationWith(ctx, tx, intent.ID); errors.Is(err, sql.ErrNoRows) {
		if err := checkProvisionCapacityTx(ctx, tx, maximum); err != nil {
			return execution.Operation{}, err
		}
	} else if err != nil {
		return execution.Operation{}, err
	}
	raw, err := normalizedOperationJSON(intent.Request)
	if err != nil {
		return execution.Operation{}, err
	}
	intent.Now = time.Now().UTC()
	op, err := beginEnvironmentOperationTx(ctx, tx, intent, raw)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func checkProvisionCapacityTx(ctx context.Context, tx *sql.Tx, maximum int) error {
	if maximum <= 0 {
		return nil
	} // The engine's positive resource budget still applies.
	count, err := countSandboxOccupyingSessions(ctx, tx)
	if err != nil {
		return err
	}
	if count >= maximum {
		return &sandbox.Error{Class: sandbox.Capacity, Operation: "provision admission", Cause: fmt.Errorf("active session limit reached")}
	}
	return nil
}

func (s *Store) BeginWorkspaceProvisionPause(ctx context.Context, id, owner string) (execution.Operation, error) {
	return s.beginUnstartedEnvironmentTransition(ctx, id, owner, "pause", 0)
}
func (s *Store) BeginInitialExecutionResume(ctx context.Context, id, owner string, maximum int) (execution.Operation, error) {
	return s.beginUnstartedEnvironmentTransition(ctx, id, owner, "resume", maximum)
}
func (s *Store) beginUnstartedEnvironmentTransition(ctx context.Context, id, owner, kind string, maximum int) (execution.Operation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Operation{}, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return execution.Operation{}, err
	}
	environment, err := environmentWith(ctx, tx, id)
	if err != nil {
		return execution.Operation{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings WHERE environment_id=?`, id).Scan(&count); err != nil {
		return execution.Operation{}, err
	}
	if count != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	key := "provision-" + kind + ":" + id
	if previous, err := operationWith(ctx, tx, key); err == nil {
		if previous.Fence != environment.Fence {
			return execution.Operation{}, execution.ErrStaleOperation
		}
		return previous, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return execution.Operation{}, err
	}
	if kind == "resume" {
		if environment.ObservedState != "paused" {
			return execution.Operation{}, execution.ErrOperationConflict
		}
		if err := checkProvisionCapacityTx(ctx, tx, maximum); err != nil {
			return execution.Operation{}, err
		}
	} else if environment.ObservedState != "running" {
		return execution.Operation{}, execution.ErrOperationConflict
	}
	raw, err := json.Marshal(execution.EnvironmentRequest{Reference: sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision}})
	if err != nil {
		return execution.Operation{}, err
	}
	raw, err = normalizedOperationJSON(raw)
	if err != nil {
		return execution.Operation{}, err
	}
	op, err := beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: key, EnvironmentID: id, Kind: kind, IdempotencyKey: key, ExpectedFence: environment.Fence, Request: raw, Now: time.Now().UTC()}, raw)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}

// BeginGenerationConfiguration reserves the intent to bring a retained paused
// environment to one generation's declaration. The operation is keyed by the
// complete immutable declaration digest, so a retry replays the same immutable request
// and a different declaration is a different operation rather than an overwrite.
//
// Reconfiguring is refused while any execution binding exists: the declaration a
// running process was accepted under cannot change underneath it.
func (s *Store) BeginGenerationConfiguration(ctx context.Context, id, owner string, configuration execution.GenerationConfiguration) (execution.Operation, error) {
	if configuration.SpecDigest == "" {
		return execution.Operation{}, fmt.Errorf("generation configuration spec digest required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Operation{}, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return execution.Operation{}, err
	}
	environment, err := environmentWith(ctx, tx, id)
	if err != nil {
		return execution.Operation{}, err
	}
	if environment.SandboxID == "" || environment.ObservedState != "paused" {
		return execution.Operation{}, execution.ErrOperationConflict
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings WHERE environment_id=? AND state!='retired'`, id).Scan(&active); err != nil {
		return execution.Operation{}, err
	}
	if active != 0 {
		return execution.Operation{}, execution.ErrBusy
	}
	declaration, err := canonicaljson.Marshal(configuration)
	if err != nil {
		return execution.Operation{}, err
	}
	key := fmt.Sprintf("configure:%s:%x", id, sha256.Sum256(declaration))
	raw, err := json.Marshal(execution.EnvironmentRequest{
		Reference:     sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision},
		Network:       &configuration.Network,
		Mounts:        configuration.Mounts,
		Configuration: &configuration,
	})
	if err != nil {
		return execution.Operation{}, err
	}
	raw, err = normalizedOperationJSON(raw)
	if err != nil {
		return execution.Operation{}, err
	}
	if previous, err := operationWith(ctx, tx, key); err == nil {
		// A replay must reuse the original request: the reference it pinned is the
		// revision the engine will validate, and refreshing it would dispatch a new
		// mutation under an old operation identity.
		if !bytes.Equal(previous.Request, raw) && previous.Fence == environment.Fence {
			return execution.Operation{}, execution.ErrOperationConflict
		}
		if previous.Fence != environment.Fence && previous.State != execution.OperationSucceeded {
			return execution.Operation{}, execution.ErrStaleOperation
		}
		return previous, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return execution.Operation{}, err
	}
	op, err := beginEnvironmentOperationTx(ctx, tx, execution.OperationIntent{ID: key, EnvironmentID: id, Kind: "configure", IdempotencyKey: key, ExpectedFence: environment.Fence, Request: raw, Now: time.Now().UTC()}, raw)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}
