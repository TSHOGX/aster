package product

import (
	"aster/contracts/evolution"
	"encoding/json"
	"harness-platform/orchestrator/internal/evaluationclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvolutionWorkerNeedsNoInteractiveSession(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "evaluation-space", OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: "adopted"})
	}))
	defer server.Close()
	client, err := evaluationclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	// Worker has no session token and reconstructs work solely from durable state.
	worker := &EvolutionWorker{Store: s, Evaluation: client}
	must(t, worker.Step(ctx))
	worker = &EvolutionWorker{Store: s, Evaluation: client}
	must(t, worker.Step(ctx))
	if calls != 1 {
		t.Fatalf("accepted work dispatched again: %d", calls)
	}
	var count int
	must(t, s.reader.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND participant='evaluation-space' AND state='accepted'", pending.OperationID).Scan(&count))
	if count != 1 {
		t.Fatal("worker did not persist receipt")
	}
	config, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if config.ActiveRevision != 0 {
		t.Fatal("worker bypassed Studio receipt")
	}
}
