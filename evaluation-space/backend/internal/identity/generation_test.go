package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func TestGenerationTransportPinsAssignmentAndRejectsCentralSubstitution(t *testing.T) {
	assignment := evolution.GenerationAssignment{RunID: "run", GroupID: "group", BindingID: "binding", OccurrenceID: "occurrence", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Pins: evolution.RunPins{Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "lessons", Profile: "authored-v1", ReservationGeneration: 1}}, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}}
	digest, err := assignment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	grant := evolution.GenerationGrant{ID: "grant", RunID: "run", GroupID: "group", AssignmentDigest: digest}
	secret := strings.Repeat("s", 32)
	mode := "ok"
	observed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != secret || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
			t.Error("wrong generation service identity")
		}
		if mode == "denied" {
			w.WriteHeader(403)
			return
		}
		if mode == "offline" {
			w.WriteHeader(503)
			return
		}
		switch r.URL.Path {
		case "/api/identity/v1/generation/admit":
			value, err := evolution.Decode[evolution.GenerationAssignment](r.Body)
			if err != nil || value.RunID != "run" {
				t.Error("lost assignment")
			}
			copy := grant
			if mode == "substitute" {
				copy.AssignmentDigest = evolution.Digest([]byte("other"))
			}
			_ = json.NewEncoder(w).Encode(copy)
		case "/api/identity/v1/generation/authority":
			scope, err := evolution.Decode[evolution.GenerationGrantScope](r.Body)
			if err != nil || scope.Grant != grant || scope.Fence != 7 {
				t.Error("lost authority scope", scope, err)
			}
			copy := assignment
			if mode == "substitute" {
				copy.OccurrenceID = "other"
			}
			_ = json.NewEncoder(w).Encode(copy)
		case "/api/identity/v1/generation/observe":
			value, err := evolution.Decode[evolution.GenerationObservation](r.Body)
			if err != nil || value.Grant != grant || value.Fence != 7 || value.State != "completed" {
				t.Error("lost observation")
			}
			observed++
			w.WriteHeader(204)
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	authority, err := NewProjectionAuthority(ProjectionConfig{Endpoint: server.URL, ClientID: "evaluation-space", ClientSecret: secret, CentralSecret: strings.Repeat("c", 32)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := authority.AdmitGeneration(context.Background(), assignment)
	if err != nil || got != grant {
		t.Fatal(got, err)
	}
	scope := evolution.GenerationGrantScope{Grant: grant, Action: "handoff", Fence: 7}
	if _, err = authority.ResolveGeneration(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	if err = authority.ObserveGeneration(context.Background(), evolution.GenerationObservation{Grant: grant, Fence: 7, State: "completed"}); err != nil || observed != 1 {
		t.Fatal(err)
	}
	if err = authority.SettleGeneration(context.Background(), grant, 7, 3, 4); err != nil {
		t.Fatal(err)
	}
	mode = "substitute"
	if _, err = authority.AdmitGeneration(context.Background(), assignment); !errors.Is(err, ErrUnavailable) {
		t.Fatal("substituted grant", err)
	}
	if _, err = authority.ResolveGeneration(context.Background(), scope); !errors.Is(err, ErrUnavailable) {
		t.Fatal("substituted assignment", err)
	}
	for _, test := range []struct {
		mode string
		want error
	}{{"denied", ErrUnauthorized}, {"offline", ErrUnavailable}} {
		mode = test.mode
		if _, err = authority.ResolveGeneration(context.Background(), scope); !errors.Is(err, test.want) {
			t.Fatal(mode, err)
		}
	}
}
