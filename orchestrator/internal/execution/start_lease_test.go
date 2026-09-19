package execution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartLeaseLossCancelsWorkAndCannotBeRevived(t *testing.T) {
	var renewals atomic.Int32
	ctx, lease, err := HoldStartLease(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("renewal is not bounded")
		}
		if renewals.Add(1) > 1 {
			return errors.New("owner fence changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("lease loss did not cancel backend work")
	}
	if !errors.Is(lease.Err(), ErrStartLeaseLost) {
		t.Fatalf("missing ownership failure: %v", lease.Err())
	}
	count := renewals.Load()
	if err := lease.EnsureOwned(); !errors.Is(err, ErrStartLeaseLost) {
		t.Fatalf("revived failed ownership: %v", err)
	}
	if renewals.Load() != count {
		t.Fatal("renewed after losing authority")
	}
}

func TestStartLeaseRejectsFailedInitialFence(t *testing.T) {
	ctx, lease, err := HoldStartLease(context.Background(), time.Minute, func(context.Context) error { return errors.New("stale owner") })
	if !errors.Is(err, ErrStartLeaseLost) {
		t.Fatalf("accepted stale initial fence: %v", err)
	}
	lease.Stop()
	if ctx.Err() == nil {
		t.Fatal("backend context remained live")
	}
}

func TestStartLeaseStopsWithParentAndNilLeaseNeedsNoRenewal(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, lease, err := HoldStartLease(parent, time.Minute, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	lease.Stop()
	if ctx.Err() == nil {
		t.Fatal("parent cancellation did not propagate")
	}
	if err := lease.EnsureOwned(); !errors.Is(err, context.Canceled) {
		t.Fatalf("renewed stopped lease: %v", err)
	}
	var absent *StartLease
	absent.Stop()
	if absent.Err() != nil || absent.EnsureOwned() != nil {
		t.Fatal("existing execution required a new lease")
	}
}

func TestBlockedRenewalCancelsWithinLeaseBudget(t *testing.T) {
	var renewals atomic.Int32
	ctx, lease, err := HoldStartLease(context.Background(), 40*time.Millisecond, func(ctx context.Context) error {
		if renewals.Add(1) == 1 {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("blocked renewal exceeded lease budget")
	}
	if !errors.Is(lease.Err(), ErrStartLeaseLost) {
		t.Fatalf("blocked renewal did not lose authority: %v", lease.Err())
	}
}
