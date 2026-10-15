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

type callAuthorityFixture struct{ denied bool }

func (a *callAuthorityFixture) AuthorizeProviderCall(context.Context, RunLease, ProviderCall) error {
	if a.denied {
		return ErrConflict
	}
	return nil
}

type providerFixture struct {
	outcome   ProviderOutcome
	submitted int
	committed bool
	key       string
}

func (p *providerFixture) Lookup(_ context.Context, call ProviderCall) (ProviderOutcome, error) {
	if !p.committed {
		return ProviderOutcome{}, ErrProviderCallNotFound
	}
	if p.key != call.IdempotencyKey {
		return ProviderOutcome{}, ErrConflict
	}
	return p.outcome, nil
}
func (p *providerFixture) Submit(_ context.Context, call ProviderCall, _ []byte, _ RunUsage) (ProviderOutcome, error) {
	p.submitted++
	p.committed = true
	p.key = call.IdempotencyKey
	return ProviderOutcome{}, errors.New("lost provider reply")
}
func TestProviderExecutionRecoversLostReplyWithoutDuplicateSubmission(t *testing.T) {
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
	call := ProviderCall{ID: "call", RunID: "run", Request: artifact}
	provider := &providerFixture{outcome: ProviderOutcome{CallID: "call", ProviderRequestID: "remote-request", State: "completed", Usage: RunUsage{4, 7}, Output: &artifact}}
	authority := &callAuthorityFixture{denied: true}
	if _, err = s.ExecuteProviderCall(ctx, lease, call, RunUsage{8, 15}, provider, authority); err == nil || provider.submitted != 0 {
		t.Fatal("unauthorized dispatch", err)
	}
	authority.denied = false
	for i := 0; i < 2; i++ {
		outcome, err := s.ExecuteProviderCall(ctx, lease, call, RunUsage{8, 15}, provider, authority)
		if err != nil || outcome.ProviderRequestID != "remote-request" {
			t.Fatal(outcome, err)
		}
	}
	if provider.submitted != 1 || provider.key == "" {
		t.Fatal("duplicate provider effect", provider.submitted)
	}
	holds, err := s.PendingRunBudgets(ctx, lease, "", 10)
	if err != nil || len(holds.Reservations) != 0 {
		t.Fatal(holds, err)
	}
	changed := call
	changed.Request.ID = "other"
	if _, err = s.ExecuteProviderCall(ctx, lease, changed, RunUsage{8, 15}, provider, authority); err != ErrConflict {
		t.Fatal("retained outcome returned for substituted request", err)
	}
	authority.denied = true
	if _, err = s.ExecuteProviderCall(ctx, lease, call, RunUsage{8, 15}, provider, authority); err == nil {
		t.Fatal("retained outcome bypassed current authority")
	}
}
