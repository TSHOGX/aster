package sessionfiles

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/workspace"
	"testing"
	"time"
)

type scanRepository struct {
	after     []string
	committed []string
	cancel    context.CancelFunc
}

func (r *scanRepository) WorkspaceInventorySessions(_ context.Context, after string, _ int) ([]string, error) {
	r.after = append(r.after, after)
	if after == "" {
		return []string{"broken", "ready"}, nil
	}
	return nil, nil
}
func (r *scanRepository) BeginWorkspaceInventory(_ context.Context, session, owner, id string) (workspace.InventoryScan, error) {
	return workspace.InventoryScan{ID: id, SessionID: session, Owner: owner}, nil
}
func (r *scanRepository) CommitWorkspaceInventory(_ context.Context, p workspace.InventoryScan, _ []workspace.File) ([]int64, error) {
	r.committed = append(r.committed, p.SessionID)
	r.cancel()
	return []int64{3}, nil
}

type scanSource struct{}

func (scanSource) Inventory(_ context.Context, session, _ string, _ int) ([]workspace.File, error) {
	if session == "broken" {
		return nil, errors.New("incomplete guest listing")
	}
	return []workspace.File{{Path: "file", Size: 1, ModifiedAt: time.Now()}}, nil
}
func TestPeriodicInventoryContinuesPastFailedSessionWithoutPublishingPartialScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repository := &scanRepository{cancel: cancel}
	published := 0
	failures := 0
	scanner := Scanner{Repository: repository, Source: scanSource{}, Owner: "owner", Limit: 100, AfterCommit: func(context.Context, int64) { published++ }}
	err := scanner.Run(ctx, time.Millisecond, time.Second, func(session string, err error) {
		if session != "broken" {
			t.Errorf("wrong failed session: %s", session)
		}
		failures++
	})
	if !errors.Is(err, context.Canceled) || failures != 1 || published != 1 || len(repository.committed) != 1 || repository.committed[0] != "ready" {
		t.Fatalf("inventory backstop: %v failures=%d events=%d committed=%v", err, failures, published, repository.committed)
	}
}
