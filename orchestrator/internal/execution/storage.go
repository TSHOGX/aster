package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type StorageBinding struct {
	SessionID, EnvironmentID, Purpose, StoreID, VolumeID string
	Capacity                                             int64
}
type VolumeBackend interface {
	Identity(context.Context) (sandbox.Identity, error)
	CreateVolume(context.Context, string, int64) (sandbox.Volume, error)
}
type StorageRepository interface {
	FindEnvironmentOperation(context.Context, string) (Operation, bool, error)
	GetEnvironmentBinding(context.Context, string) (Environment, error)
	GetStorageBinding(context.Context, string, string) (StorageBinding, bool, error)
	RecordStorageBinding(context.Context, StorageBinding) error
	BeginEnvironmentOperation(context.Context, OperationIntent) (Operation, error)
	RecordEnvironmentOperationOutcome(context.Context, OperationOutcome) error
}

type StorageService struct {
	Repository StorageRepository
	Backend    VolumeBackend
}

// EnsureVolume retries an immutable keyed intent and records the independent
// storage binding. A crash after remote creation does not allocate another
// volume; a crash after operation commit replays its result before binding it.
func (s StorageService) EnsureVolume(ctx context.Context, environmentID, purpose string, capacity int64) (StorageBinding, error) {
	if s.Repository == nil || s.Backend == nil || capacity <= 0 || (purpose != "workspace" && purpose != "bridge") {
		return StorageBinding{}, fmt.Errorf("session volume service, purpose and positive capacity required")
	}
	environment, err := s.Repository.GetEnvironmentBinding(ctx, environmentID)
	if err != nil {
		return StorageBinding{}, err
	}
	identity, err := s.Backend.Identity(ctx)
	if err != nil {
		return StorageBinding{}, err
	}
	if identity.StoreID != environment.StoreID {
		return StorageBinding{}, ErrBackendIdentityChanged
	}
	if existing, found, err := s.Repository.GetStorageBinding(ctx, environment.SessionID, purpose); err != nil {
		return StorageBinding{}, err
	} else if found {
		if existing.StoreID != environment.StoreID || existing.EnvironmentID != environment.ID || existing.Capacity != capacity {
			return StorageBinding{}, ErrOperationConflict
		}
		return existing, nil
	}
	raw, _ := json.Marshal(struct {
		Purpose  string
		Capacity int64
	}{purpose, capacity})
	id := "volume:" + environment.ID + ":" + purpose
	// A repeated deterministic operation may already own the previous fence.
	// The repository verifies its exact request and stored reservation on replay.
	intent := OperationIntent{ID: id, EnvironmentID: environment.ID, Kind: "create_volume", IdempotencyKey: id, ExpectedFence: environment.Fence, Request: raw, Now: time.Now().UTC()}
	if previous, found, err := s.Repository.FindEnvironmentOperation(ctx, id); err != nil {
		return StorageBinding{}, err
	} else if found {
		intent.ExpectedFence = previous.Fence - 1
	}
	operation, err := s.Repository.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		return StorageBinding{}, err
	}
	binding := StorageBinding{SessionID: environment.SessionID, EnvironmentID: environment.ID, Purpose: purpose, StoreID: environment.StoreID, Capacity: capacity}
	if operation.State == OperationSucceeded {
		var volume sandbox.Volume
		if err := json.Unmarshal(operation.Result, &volume); err != nil {
			return binding, err
		}
		binding.VolumeID = volume.VolumeID
		return binding, s.Repository.RecordStorageBinding(ctx, binding)
	}
	if operation.State == OperationFailed {
		return binding, fmt.Errorf("volume creation previously failed: %s", operation.Error)
	}
	if operation.State == OperationPending {
		if err := s.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: id, Fence: operation.Fence, State: OperationIssued}); err != nil {
			return binding, err
		}
	}
	volume, err := s.Backend.CreateVolume(ctx, id, capacity)
	if err != nil {
		state := OperationUncertain
		if sandbox.Is(err, sandbox.Invalid) || sandbox.Is(err, sandbox.Capacity) {
			state = OperationFailed
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return binding, errors.Join(err, s.Repository.RecordEnvironmentOperationOutcome(recordCtx, OperationOutcome{ID: id, Fence: operation.Fence, State: state, Error: err.Error()}))
	}
	if volume.StoreID != environment.StoreID || volume.VolumeID == "" || volume.Capacity != capacity {
		return binding, ErrBackendIdentityChanged
	}
	result, _ := json.Marshal(volume)
	if err := s.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: id, Fence: operation.Fence, State: OperationSucceeded, Result: result}); err != nil {
		return binding, err
	}
	binding.VolumeID = volume.VolumeID
	return binding, s.Repository.RecordStorageBinding(ctx, binding)
}
