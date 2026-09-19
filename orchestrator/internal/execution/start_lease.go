// Package execution owns application lifecycle coordination independently of
// HTTP transport and sandbox substrate mechanisms.
package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrStartLeaseLost = errors.New("generation start lease lost")

// StartLease renews application ownership while a backend prepares and starts
// an execution. A nil lease represents an already-owned, unchanged execution.
type StartLease struct {
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	renew          func(context.Context) error
	renewalTimeout time.Duration
	renewalMu      sync.Mutex
	mu             sync.Mutex
	failure        error
}

func HoldStartLease(ctx context.Context, ttl time.Duration, renew func(context.Context) error) (context.Context, *StartLease, error) {
	if renew == nil || ttl <= 0 {
		return ctx, nil, fmt.Errorf("start lease requires renewal and positive TTL")
	}
	startCtx, cancel := context.WithCancel(ctx)
	renewalTimeout := ttl / 2
	if renewalTimeout <= 0 {
		renewalTimeout = ttl
	}
	if renewalTimeout > 5*time.Second {
		renewalTimeout = 5 * time.Second
	}
	lease := &StartLease{ctx: startCtx, cancel: cancel, done: make(chan struct{}), renew: renew, renewalTimeout: renewalTimeout}
	if err := lease.EnsureOwned(); err != nil {
		cancel()
		close(lease.done)
		return startCtx, lease, err
	}
	interval := ttl / 2
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		defer close(lease.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-startCtx.Done():
				return
			case <-ticker.C:
				if err := lease.EnsureOwned(); err != nil {
					return
				}
			}
		}
	}()
	return startCtx, lease, nil
}

// EnsureOwned checks the durable fence again at a lifecycle boundary. Failed
// renewal is sticky: later success cannot revive a start that lost authority.
func (k *StartLease) EnsureOwned() error {
	if k == nil {
		return nil
	}
	k.renewalMu.Lock()
	defer k.renewalMu.Unlock()
	if err := k.Err(); err != nil {
		return err
	}
	if err := k.ctx.Err(); err != nil {
		return err
	}
	renewCtx, cancel := context.WithTimeout(context.Background(), k.renewalTimeout)
	defer cancel()
	if err := k.renew(renewCtx); err != nil {
		k.mu.Lock()
		if k.failure == nil {
			k.failure = fmt.Errorf("%w: %v", ErrStartLeaseLost, err)
		}
		failure := k.failure
		k.mu.Unlock()
		k.cancel()
		return failure
	}
	if err := k.Err(); err != nil {
		return err
	}
	return k.ctx.Err()
}
func (k *StartLease) Err() error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.failure
}
func (k *StartLease) Stop() {
	if k == nil {
		return
	}
	k.cancel()
	<-k.done
}
