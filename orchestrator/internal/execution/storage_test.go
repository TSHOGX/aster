package execution_test

import (
	"context"
	"errors"
	"testing"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

type volumeBackend struct {
	calls    int
	lose     bool
	identity string
	keys     []string
}

func (b *volumeBackend) Identity(context.Context) (sandbox.Identity, error) {
	return sandbox.Identity{StoreID: b.identity, Version: "pinned", ManualColdRecovery: true}, nil
}
func (b *volumeBackend) CreateVolume(_ context.Context, key string, capacity int64) (sandbox.Volume, error) {
	b.calls++
	b.keys = append(b.keys, key)
	if b.lose {
		b.lose = false
		return sandbox.Volume{}, &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("lost volume response")}
	}
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: b.identity, VolumeID: "volume-" + key}, Capacity: capacity}, nil
}

type bindingWriteFailure struct {
	execution.StorageRepository
	fail bool
}

func (r *bindingWriteFailure) RecordStorageBinding(ctx context.Context, binding execution.StorageBinding) error {
	if r.fail {
		r.fail = false
		return errors.New("injected binding commit failure")
	}
	return r.StorageRepository.RecordStorageBinding(ctx, binding)
}

func TestSessionVolumeRetryPreservesKeyAndNeverReprovisionsBoundData(t *testing.T) {
	_, store, _ := serviceFixture(t)
	backend := &volumeBackend{lose: true, identity: "store"}
	service := execution.StorageService{Repository: store, Backend: backend}
	if _, err := service.EnsureVolume(context.Background(), "environment", "workspace", 64<<20); !sandbox.Is(err, sandbox.Unavailable) {
		t.Fatalf("lost response: %v", err)
	}
	bound, err := service.EnsureVolume(context.Background(), "environment", "workspace", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 || backend.keys[0] != backend.keys[1] {
		t.Fatalf("retry changed remote identity: %v", backend.keys)
	}
	again, err := service.EnsureVolume(context.Background(), "environment", "workspace", 64<<20)
	if err != nil || again != bound || backend.calls != 2 {
		t.Fatalf("existing volume reprovisioned: %+v %v", again, err)
	}
	backend.identity = "rebuilt"
	if _, err := service.EnsureVolume(context.Background(), "environment", "workspace", 64<<20); !errors.Is(err, execution.ErrBackendIdentityChanged) {
		t.Fatalf("bound data reinterpreted after engine rebuild: %v", err)
	}
}
func TestVolumeOperationCommitCanRecoverMissingBindingWithoutRemoteCreate(t *testing.T) {
	_, store, _ := serviceFixture(t)
	backend := &volumeBackend{identity: "store"}
	repository := &bindingWriteFailure{StorageRepository: store, fail: true}
	service := execution.StorageService{Repository: repository, Backend: backend}
	if _, err := service.EnsureVolume(context.Background(), "environment", "bridge", 64<<20); err == nil {
		t.Fatal("binding commit fault ignored")
	}
	bound, err := service.EnsureVolume(context.Background(), "environment", "bridge", 64<<20)
	if err != nil || bound.VolumeID == "" || backend.calls != 1 {
		t.Fatalf("operation replay recreated volume: %+v calls=%d %v", bound, backend.calls, err)
	}
}
