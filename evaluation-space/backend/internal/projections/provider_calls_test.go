package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProviderCallIdentitySurvivesTakeover(t *testing.T) {
	t.Run("completed", func(t *testing.T) { providerCallIdentitySurvivesTakeover(t, false) })
	t.Run("retryable-failure", func(t *testing.T) { providerCallIdentitySurvivesTakeover(t, true) })
}

func providerCallIdentitySurvivesTakeover(t *testing.T, retryable bool) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
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
	if _, err = s.PrepareProviderCall(ctx, lease, "call", artifact); err == nil {
		t.Fatal("unreserved provider call prepared")
	}
	if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{8, 15}); err != nil {
		t.Fatal(err)
	}
	call, err := s.PrepareProviderCall(ctx, lease, "call", artifact)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.PrepareProviderCall(ctx, lease, "call", artifact)
	if err != nil || replay != call {
		t.Fatal("request replay", err)
	}
	artifact.ID = "other-request"
	if err = s.SealArtifact(ctx, artifact, strings.NewReader("request")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareProviderCall(ctx, lease, "call", artifact); err != ErrConflict {
		t.Fatal("request substituted under held call", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='run'"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.ClaimRun(ctx, "run", "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := s.ProviderCall(ctx, replacement, "call")
	if err != nil || retained != call {
		t.Fatal("takeover changed provider identity", retained, err)
	}
	if _, err = s.ProviderCall(ctx, lease, "call"); err != ErrConflict {
		t.Fatal("stale worker recovered request", err)
	}
	if err = s.SettleRunBudget(ctx, replacement, "call", RunUsage{4, 7}); err != ErrConflict {
		t.Fatal("prepared call settled without outcome", err)
	}
	outcome := ProviderOutcome{CallID: "call", ProviderRequestID: "provider-request", State: "completed", Usage: RunUsage{4, 7}, Output: &call.Request}
	invalid := outcome
	invalid.Retryable = true
	if err = s.RecordProviderOutcome(ctx, replacement, invalid); err == nil {
		t.Fatal("completed retry hint accepted")
	}
	invalid.State = "failed"
	if err = s.RecordProviderOutcome(ctx, replacement, invalid); err == nil {
		t.Fatal("failed outcome with output accepted")
	}
	if retryable {
		outcome.State, outcome.Output, outcome.Retryable = "failed", nil, true
	}
	if err = s.RecordProviderOutcome(ctx, replacement, outcome); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordProviderOutcome(ctx, replacement, outcome); err != nil {
		t.Fatal("outcome retry", err)
	}
	if retryable {
		changed := outcome
		changed.Retryable = false
		if err = s.RecordProviderOutcome(ctx, replacement, changed); err != ErrConflict {
			t.Fatal("retry eligibility rewritten", err)
		}
	}
	changed := outcome
	changed.Usage.Tokens++
	if err = s.RecordProviderOutcome(ctx, replacement, changed); err != ErrConflict {
		t.Fatal("provider outcome rewritten", err)
	}
	holds, err := s.PendingRunBudgets(ctx, replacement, "", 10)
	if err != nil || len(holds.Reservations) != 0 {
		t.Fatal("outcome did not settle hold", holds, err)
	}
	var tokens, cost int64
	if err = s.db.QueryRowContext(ctx, "SELECT tokens,cost_micros FROM runs WHERE id='run'").Scan(&tokens, &cost); err != nil || tokens != 4 || cost != 7 {
		t.Fatal("usage double charged", tokens, cost, err)
	}

	if _, err = s.db.ExecContext(ctx, "UPDATE runs SET lease_until=0 WHERE id='run'"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := s.ClaimRun(ctx, "run", "outcome-recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ProviderOutcome(ctx, recovery, "call")
	if err != nil || !reflect.DeepEqual(recovered, outcome) {
		t.Fatal("lost retained result", recovered, err)
	}
	if _, err = s.ProviderOutcome(ctx, replacement, "call"); err != ErrConflict {
		t.Fatal("old worker read recovered outcome", err)
	}
	if _, err = s.ProviderOutcome(ctx, recovery, "unknown"); err != ErrNotFound {
		t.Fatal("unknown outcome", err)
	}

}
