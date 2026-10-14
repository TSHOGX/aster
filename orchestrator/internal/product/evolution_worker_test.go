package product

import (
	"aster/contracts/evolution"
	"encoding/json"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/studioclient"
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

func TestEvolutionWorkerCoordinatesBothParticipants(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	state := ""
	evaluated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		participant := "studio"
		if strings.HasPrefix(r.URL.Path, "/evaluation-space/") {
			if state != "reserved" {
				t.Error("evaluation preceded reservation")
			}
			evaluated = true
			participant = "evaluation-space"
		} else {
			switch strings.TrimPrefix(r.URL.Path, "/volumes-studio/internal/v1/evolution/") {
			case "lookup":
				if state == "" {
					http.NotFound(w, r)
					return
				}
			case "reserve":
				state = "reserved"
			case "adopt":
				if !evaluated {
					t.Error("adopted before evaluation")
				}
				state = "adopted"
			}
		}
		receiptState := state
		if participant == "evaluation-space" {
			receiptState = "adopted"
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: participant, OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: receiptState})
	}))
	defer server.Close()
	evaluation, err := evaluationclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	studio, err := studioclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	worker := &EvolutionWorker{Store: s, Evaluation: evaluation, Studio: studio}
	must(t, worker.Step(ctx))
	active, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if active.State != "active" || active.ActiveRevision != 1 || state != "adopted" {
		t.Fatal(active, state)
	}
}

func TestEvolutionWorkerRecordsExplicitCatalogRejection(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	client, err := evaluationclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	worker := &EvolutionWorker{Store: s, Evaluation: client}
	must(t, worker.Step(ctx))
	failed, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if failed.State != "failed" || failed.ActiveRevision != 0 || failed.OperationID != pending.OperationID {
		t.Fatal(failed)
	}
	worker = &EvolutionWorker{Store: s, Evaluation: client}
	must(t, worker.Step(ctx))
	if calls != 2 {
		t.Fatal("failed operation retried without instruction", calls)
	}
	var code string
	must(t, s.reader.QueryRowContext(ctx, "SELECT error FROM evolution_activation_receipts WHERE operation_id=?", pending.OperationID).Scan(&code))
	if code != "catalog_rejected" {
		t.Fatal(code)
	}
	must(t, s.RetryEvolutionActivation(ctx, token, pending.OperationID))
	must(t, worker.Step(ctx))
	if calls != 4 {
		t.Fatal("explicit retry not dispatched", calls)
	}
}
