package evaluationclient

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParticipantReceiptIdentity(t *testing.T) {
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	p := Projection{"operation", evolution.Digest(raw), config}
	for _, mismatch := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if !ok || user != "central" || password != strings.Repeat("x", 32) {
				t.Error("missing service credentials")
			}
			receipt := evolution.ActivationReceipt{Participant: "evaluation-space", OperationID: p.OperationID, ConfigurationDigest: p.Digest, Generation: 1, State: "adopted"}
			if mismatch {
				receipt.Generation = 2
			}
			json.NewEncoder(w).Encode(receipt)
		}))
		client, err := New(server.URL, strings.Repeat("x", 32))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Adopt(context.Background(), p)
		if (err != nil) != mismatch {
			t.Fatal("adoption receipt check", err)
		}
		_, err = client.Lookup(context.Background(), p)
		if (err != nil) != mismatch {
			t.Fatal("lookup receipt check", err)
		}
		server.Close()
	}
}

func TestReconcileLostAdoptionReply(t *testing.T) {
	config := evolution.Configuration{GroupID: "alpha", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	p := Projection{"operation", evolution.Digest(raw), config}
	adopted, posts := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			adopted = true
			// The participant committed before the connection dropped.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		if !adopted {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "evaluation-space", OperationID: p.OperationID, ConfigurationDigest: p.Digest, Generation: 1, State: "adopted"})
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.Reconcile(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 {
		t.Fatalf("adoption dispatched %d times", posts)
	}
}

func TestReconcileDoesNotAdoptAfterUnavailableLookup(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Reconcile(context.Background(), Projection{}); err == nil {
		t.Fatal("unavailable lookup accepted")
	}
	if posts != 0 {
		t.Fatal("adopted without establishing participant state")
	}
}
