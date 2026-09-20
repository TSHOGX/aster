package server

import (
	"context"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/store"
	"sync"
	"time"
)

// RunRetainedMaintenance owns product housekeeping and durable projection only.
// Environment/agent/volume mechanics belong to the retained runtime services.
func (s *Server) RunRetainedMaintenance(ctx context.Context, interval, timeout time.Duration) error {
	if s.ownerUUID == "" || s.executions == nil || interval <= 0 || timeout <= 0 {
		return fmt.Errorf("retained maintenance requires owner, controller, bounds and indefinite retention")
	}
	tasks := map[string]func(context.Context) error{
		"projection": func(ctx context.Context) error { return s.projectBridgeRecords(ctx, 128) },
		"product_cleanup": func(ctx context.Context) error {
			now := time.Now().UTC()
			if _, err := s.store.SweepExpiredAuthSessions(ctx, now); err != nil {
				return err
			}
			if _, err := s.store.CancelTerminalSessionPendingTurns(ctx, now); err != nil {
				return err
			}
			if err := s.flushShareLinkAccess(ctx); err != nil {
				return err
			}
			s.sweepExpiredShareLinks(ctx, now)
			s.loginLimiter.GC()
			s.ipLimiter.GC()
			_, err := s.store.PruneEvents(ctx, store.PruneEventsParams{RetentionWindow: s.cfg.Harness.Events.RetentionWindow.Duration, RetentionRows: s.cfg.Harness.Events.RetentionRows, Now: now})
			s.touchMaintenanceHeartbeat(now)
			return err
		},
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); s.runScheduler(ctx) }()
	for name, task := range tasks {
		workers.Add(1)
		go func(name string, task func(context.Context) error) {
			defer workers.Done()
			for ctx.Err() == nil {
				action, cancel := context.WithTimeout(ctx, timeout)
				err := task(action)
				cancel()
				if err != nil && !errors.Is(err, context.Canceled) {
					s.log.Warn("retained product maintenance failed", "stage", name, "error", err)
				}
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(name, task)
	}
	workers.Wait()
	return ctx.Err()
}
