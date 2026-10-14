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
	must(t, s.AcceptEvolutionParticipant(ctx, token, pending.OperationID, "studio", pending.DesiredDigest))
	active, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	if active.ActiveDigest != pending.DesiredDigest {
		t.Fatal(active)
	}
}
