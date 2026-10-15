package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderCallIdentitySurvivesTakeover(t *testing.T) {
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
}
