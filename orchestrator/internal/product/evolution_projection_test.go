package product

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/evaluationclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvolutionReconcileRecordsVerifiedProjection(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"save", 0, activationConfigFixture(1, true)})
	must(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		var p evaluationclient.Projection
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if p.OperationID != pending.OperationID || p.Digest != pending.DesiredDigest || p.Configuration.Budget.MaxTokens != 100 {
			t.Error("wrong projection", p)
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "evaluation-space", OperationID: p.OperationID, ConfigurationDigest: p.Digest, Generation: p.Configuration.Revision, State: "adopted"})
	}))
	defer server.Close()
	client, err := evaluationclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	must(t, s.ReconcileEvolutionProjection(ctx, token, pending.OperationID, client))
	if _, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID); err != ErrBusy {
		t.Fatal("settled without Studio", err)
	}
	must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, "studio", pending.DesiredDigest))
	active, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	if active.ActiveDigest != pending.DesiredDigest {
		t.Fatal(active)
	}
}

func TestEvolutionProjectionFencesFailedDispatchAndLateReceipt(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"save", 0, activationConfigFixture(1, true)})
	must(t, err)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// A different participant fails while this request is in flight.
		if err := s.FailEvolutionParticipant(ctx, token, pending.OperationID, "studio", pending.DesiredDigest, "offline"); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "evaluation-space", OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: "adopted"})
	}))
	defer server.Close()
	client, err := evaluationclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	if err = s.ReconcileEvolutionProjection(ctx, token, pending.OperationID, client); err != ErrConflict {
		t.Fatal("late receipt accepted", err)
	}
	if err = s.ReconcileEvolutionProjection(ctx, token, pending.OperationID, client); err != ErrConflict {
		t.Fatal("failed operation dispatched", err)
	}
	if calls != 1 {
		t.Fatalf("failed operation made %d requests", calls)
	}
	var receipts int
	must(t, s.reader.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND participant='evaluation-space'", pending.OperationID).Scan(&receipts))
	if receipts != 0 {
		t.Fatal("late receipt persisted")
	}
}

func TestEvolutionReceiptRequiresExactGeneration(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"save", 0, activationConfigFixture(1, true)})
	must(t, err)
	receipt := evolution.ActivationReceipt{Participant: "evaluation-space", OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 2, State: "adopted"}
	if err = s.acceptEvolutionParticipant(ctx, token, receipt); err == nil {
		t.Fatal("wrong generation accepted")
	}
	receipt.Generation = 1
	receipt.State = "failed"
	if err = s.acceptEvolutionParticipant(ctx, token, receipt); err != ErrInvalid {
		t.Fatal("wrong state accepted", err)
	}
	receipt.State = "adopted"
	must(t, s.acceptEvolutionParticipant(ctx, token, receipt))
}
