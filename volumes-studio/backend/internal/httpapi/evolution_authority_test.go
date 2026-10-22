package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"volume-studio/backend/internal/identity"
)

func TestCentralEvolutionAuthorityResolution(t *testing.T) {
	secret := strings.Repeat("s", 32)
	scope := EvolutionParticipantScope{"group", "operation", 1, "sha256:" + strings.Repeat("a", 64)}
	status, calls := 200, 0
	issuer := "https://central"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "studio" || password != secret {
			t.Error("missing registered identity credential")
		}
		if r.URL.Path != "/api/identity/v1/studio-participant-authority" {
			t.Error("wrong authority endpoint")
		}
		var input struct {
			Scope  EvolutionParticipantScope `json:"scope"`
			Action string                    `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Scope != scope || input.Action != "adopt" {
			t.Error("scope changed", input, err)
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "subject": "manager"})
	}))
	defer server.Close()
	client, err := identity.NewClient(identity.Config{Issuer: "https://central", Endpoint: server.URL, ClientID: "studio", ClientSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewCentralEvolutionAuthority(client, secret)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	if _, err = authority.AuthorizeEvolutionParticipant(r, scope, "adopt"); !errors.Is(err, ErrEvolutionParticipantDenied) || calls != 0 {
		t.Fatal("unauthenticated resolution", calls, err)
	}
	r.SetBasicAuth("central", secret)
	actor, err := authority.AuthorizeEvolutionParticipant(r, scope, "adopt")
	if err != nil || actor.Subject != "manager" || actor.Issuer != "https://central" {
		t.Fatal(actor, err)
	}
	status = 403
	if _, err = authority.AuthorizeEvolutionParticipant(r, scope, "adopt"); !errors.Is(err, ErrEvolutionParticipantDenied) {
		t.Fatal("denial became allow", err)
	}
	status = 503
	if _, err = authority.AuthorizeEvolutionParticipant(r, scope, "adopt"); !errors.Is(err, identity.ErrUnavailable) {
		t.Fatal("outage not distinguished", err)
	}
	status = 200
	issuer = "https://foreign"
	if _, err = authority.AuthorizeEvolutionParticipant(r, scope, "adopt"); !errors.Is(err, identity.ErrUnavailable) {
		t.Fatal("foreign issuer accepted", err)
	}
}
