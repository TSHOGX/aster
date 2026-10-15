package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRunDebuggerScopesBudgetAndOpaqueEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxTokens: 10, MaxCostMicros: 20, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}
	body, _ := json.Marshal(run)
	if _, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,?,?,?,?,?)", "run", "group", "binding", 1, "occurrence", []byte(`{"run":true}`), body, "queued"); err != nil {
		t.Fatal(err)
	}
	value, err := s.RunDebugger(ctx, "group", "run")
	if err != nil || value.Budget.Ceiling.Tokens != 10 || len(value.Observations) != 0 || len(value.Opaque) == 0 {
		t.Fatal(value, err)
	}
	if _, err = s.RunDebugger(ctx, "other", "run"); err != ErrNotFound {
		t.Fatal("foreign debugger scope", err)
	}
}
