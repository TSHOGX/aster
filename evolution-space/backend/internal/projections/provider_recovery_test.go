package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recoveryAuthorityFixture struct{}

func (recoveryAuthorityFixture) AuthorizeProviderRecovery(context.Context, RunLease, ProviderCall) error {
	return nil
}

type recoveryProviderFixture struct {
	confirm   bool
	cancelled int
}

func (p *recoveryProviderFixture) Lookup(context.Context, ProviderCall) (ProviderOutcome, error) {
	return ProviderOutcome{}, ErrProviderCallNotFound
}
func (p *recoveryProviderFixture) Cancel(_ context.Context, call ProviderCall) (ProviderOutcome, error) {
	p.cancelled++
	if !p.confirm {
		return ProviderOutcome{}, errors.New("cancellation uncertain")
	}
	return ProviderOutcome{CallID: call.ID, ProviderRequestID: "cancelled-request", State: "cancelled", Usage: RunUsage{}}, nil
}
func TestProviderCleanupRequiresConfirmedTerminalOutcome(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body, _ := json.Marshal(evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxTokens: 10, MaxCostMicros: 20}})
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	artifact := evolution.ArtifactRef{ID: "request", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("request")), Size: 7, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	if err = s.SealArtifact(ctx, artifact, strings.NewReader("request")); err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{8, 15}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareProviderCall(ctx, lease, "call", artifact); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ControlRun(ctx, RunControl{RequestID: "cancel", RunID: "run", ExpectedFence: lease.Fence, Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	cleanup, err := s.ClaimRun(ctx, "run", "cleanup", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	provider := &recoveryProviderFixture{}
	if _, err = s.RecoverProviderCall(ctx, cleanup, "call", provider, recoveryAuthorityFixture{}); err == nil {
		t.Fatal("uncertain cancellation released spending")
	}
	held, err := s.PendingRunBudgets(ctx, cleanup, "", 10)
	if err != nil || len(held.Reservations) != 1 {
		t.Fatal("hold lost", held, err)
	}
	provider.confirm = true
	outcome, err := s.RecoverProviderCall(ctx, cleanup, "call", provider, recoveryAuthorityFixture{})
	if err != nil || outcome.State != "cancelled" {
		t.Fatal(outcome, err)
	}
	if _, err = s.RecoverProviderCall(ctx, cleanup, "call", provider, recoveryAuthorityFixture{}); err != nil {
		t.Fatal("outcome replay", err)
	}
	if provider.cancelled != 2 {
		t.Fatal("retained outcome repeated cancellation", provider.cancelled)
	}
	if err = s.SettleRun(ctx, cleanup, "cancelled"); err != nil {
		t.Fatal(err)
	}
}
