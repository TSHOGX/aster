package product

import (
	"aster/contracts/evolution"
	"encoding/json"
	"harness-platform/orchestrator/internal/evolutionspaceclient"
	"harness-platform/orchestrator/internal/studioclient"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "evolution-space", OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: "adopted"})
	}))
	defer server.Close()
	client, err := evolutionspaceclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	// Worker has no session token and reconstructs work solely from durable state.
	worker := &EvolutionWorker{Store: s, EvolutionSpace: client}
	must(t, worker.Step(ctx))
	worker = &EvolutionWorker{Store: s, EvolutionSpace: client}
	must(t, worker.Step(ctx))
	if calls != 1 {
		t.Fatalf("accepted work dispatched again: %d", calls)
	}
	var count int
	must(t, s.reader.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND participant='evolution-space' AND state='accepted'", pending.OperationID).Scan(&count))
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
	spaceAdopted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		participant := "studio"
		if strings.HasPrefix(r.URL.Path, "/evolution-space/") {
			if state != "reserved" {
				t.Error("Evolution Space preceded reservation")
			}
			spaceAdopted = true
			participant = "evolution-space"
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
				if !spaceAdopted {
					t.Error("adopted before Evolution Space")
				}
				state = "adopted"
			}
		}
		receiptState := state
		if participant == "evolution-space" {
			receiptState = "adopted"
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: participant, OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: receiptState})
	}))
	defer server.Close()
	evolutionSpace, err := evolutionspaceclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	studio, err := studioclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	worker := &EvolutionWorker{Store: s, EvolutionSpace: evolutionSpace, Studio: studio}
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
	client, err := evolutionspaceclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	worker := &EvolutionWorker{Store: s, EvolutionSpace: client}
	must(t, worker.Step(ctx))
	failed, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if failed.State != "failed" || failed.ActiveRevision != 0 || failed.OperationID != pending.OperationID {
		t.Fatal(failed)
	}
	worker = &EvolutionWorker{Store: s, EvolutionSpace: client}
	must(t, worker.Step(ctx))
	if calls != 2 {
		t.Fatal("failed operation retried without instruction", calls)
	}
	var code string
	must(t, s.reader.QueryRowContext(ctx, "SELECT error FROM evolution_activation_receipts WHERE operation_id=?", pending.OperationID).Scan(&code))
	if code != "catalog_rejected" {
		t.Fatal(code)
	}
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, EvolutionRetryInput{RequestID: "retry", ExpectedRevision: pending.DesiredRevision}))
	must(t, worker.Step(ctx))
	if calls != 4 {
		t.Fatal("explicit retry not dispatched", calls)
	}
}

func TestEvolutionDiscardCompensatesOnlyPendingReservations(t *testing.T) {
	for _, state := range []string{"reserved", "adopted", "missing"} {
		t.Run(state, func(t *testing.T) {
			s, admin := accountStore(t)
			token := adminTestToken(t, s, admin)
			pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
			must(t, err)
			must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evolution-space", pending.DesiredDigest, "catalog_rejected"))
			input := EvolutionRetryInput{"discard", 1}
			must(t, s.DiscardEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
			if err = s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, EvolutionRetryInput{"retry", 1}); err != ErrConflict {
				t.Fatal("retry raced discard", err)
			}
			cancelled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if state == "missing" {
					http.NotFound(w, r)
					return
				}
				result := state
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					cancelled = true
					result = "released"
				}
				json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "studio", OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: result})
			}))
			defer server.Close()
			studio, err := studioclient.New(server.URL, strings.Repeat("x", 32))
			must(t, err)
			evolutionSpace, err := evolutionspaceclient.New(server.URL, strings.Repeat("x", 32))
			must(t, err)
			worker := &EvolutionWorker{Store: s, Studio: studio, EvolutionSpace: evolutionSpace}
			err = worker.Step(ctx)
			if state == "adopted" {
				if err != ErrConflict || cancelled {
					t.Fatal("adopted ownership compensated", err)
				}
			} else {
				must(t, err)
			}
			current, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
			must(t, err)
			expected := "discarded"
			if state == "adopted" {
				expected = "discarding"
			}
			if current.State != expected || current.ActiveRevision != 0 {
				t.Fatal(current)
			}
			if state == "reserved" && !cancelled {
				t.Fatal("reservation not released")
			}
			must(t, s.DiscardEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
		})
	}
}

func TestEvolutionDiscardRecoveryAfterCentralRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	s, err := Open(ctx, path)
	must(t, err)
	defer func() { s.Close() }()
	admin, err := s.BootstrapAccount(ctx, "admin", "permanent-admin-password", bootstrapSeedFixture(t))
	must(t, err)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evolution-space", pending.DesiredDigest, "catalog_rejected"))
	input := EvolutionRetryInput{"discard", 1}
	must(t, s.DiscardEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	current, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if current.State != "discarding" {
		t.Fatal("lost discard intent", current)
	}
	// Studio committed cancellation before central persisted its completion.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/lookup") {
			t.Error("already released reservation cancelled again")
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "studio", OperationID: pending.OperationID, ConfigurationDigest: pending.DesiredDigest, Generation: 1, State: "released"})
	}))
	defer server.Close()
	studio, err := studioclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	// Cleanup requires no Evolution Space connection.
	worker := &EvolutionWorker{Store: s, Studio: studio}
	must(t, worker.Step(ctx))
	must(t, s.DiscardEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
	events, err := s.EvolutionOperationEvents(ctx, token, StandardUsersGroup, pending.OperationID, 0, 100)
	must(t, err)
	expected := []string{"activating", "failed", "discarding", "discarded"}
	if len(events.Events) != len(expected) {
		t.Fatal(events)
	}
	for i, state := range expected {
		if events.Events[i].State != state || events.Events[i].Sequence != int64(i+1) {
			t.Fatal(events)
		}
	}
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	current, err = s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if current.State != "discarded" {
		t.Fatal("lost cleanup completion", current)
	}
}

func TestEvolutionStudioConflictStopsBeforeEvaluation(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasPrefix(r.URL.Path, "/evolution-space/") {
			t.Error("evolutionSpace dispatched after reservation rejection")
		}
		if strings.HasSuffix(r.URL.Path, "/lookup") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	studio, err := studioclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	evolutionSpace, err := evolutionspaceclient.New(server.URL, strings.Repeat("x", 32))
	must(t, err)
	worker := &EvolutionWorker{Store: s, Studio: studio, EvolutionSpace: evolutionSpace}
	must(t, worker.Step(ctx))
	status, err := s.EvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID)
	must(t, err)
	if status.State != "failed" || len(status.Participants) != 1 || status.Participants[0].Error != "reservation_rejected" {
		t.Fatal(status)
	}
	worker = &EvolutionWorker{Store: s, Studio: studio, EvolutionSpace: evolutionSpace}
	must(t, worker.Step(ctx))
	if calls != 2 {
		t.Fatal("conflict retried or obscured by lookup", calls)
	}
}
