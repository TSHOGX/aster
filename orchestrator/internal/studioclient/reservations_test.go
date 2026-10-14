package studioclient

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLostReserveAndAdoptRepliesReconcile(t *testing.T) {
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	p := Projection{"operation", evolution.Digest(raw), config}
	state := ""
	reserves, adopts := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "central" || password != strings.Repeat("s", 32) {
			t.Error("missing service credential")
		}
		action := strings.TrimPrefix(r.URL.Path, "/volumes-studio/internal/v1/evolution/")
		if action == "reserve" || action == "adopt" {
			if action == "reserve" {
				state = "reserved"
				reserves++
			} else {
				state = "adopted"
				adopts++
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		if state == "" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(evolution.ActivationReceipt{Participant: "studio", OperationID: p.OperationID, ConfigurationDigest: p.Digest, Generation: 1, State: state})
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Reserve(context.Background(), p)
	if err != nil || receipt.State != "reserved" {
		t.Fatal(receipt, err)
	}
	receipt, err = client.Adopt(context.Background(), p)
	if err != nil || receipt.State != "adopted" {
		t.Fatal(receipt, err)
	}
	if _, err = client.Adopt(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if reserves != 1 || adopts != 1 {
		t.Fatal("duplicate effects", reserves, adopts)
	}
	state = "released"
	if _, err = client.Reserve(context.Background(), p); err == nil {
		t.Fatal("released operation reused")
	}
}

func TestStudioReceiptSubstitutionRejected(t *testing.T) {
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	p := Projection{"operation", evolution.Digest(raw), config}
	for _, field := range []string{"participant", "operation", "digest", "generation", "state"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receipt := evolution.ActivationReceipt{Participant: "studio", OperationID: p.OperationID, ConfigurationDigest: p.Digest, Generation: 1, State: "reserved"}
				switch field {
				case "participant":
					receipt.Participant = "evaluation-space"
				case "operation":
					receipt.OperationID = "other"
				case "digest":
					receipt.ConfigurationDigest = evolution.Digest(nil)
				case "generation":
					receipt.Generation = 2
				case "state":
					receipt.State = "unknown"
				}
				json.NewEncoder(w).Encode(receipt)
			}))
			defer server.Close()
			client, err := New(server.URL, strings.Repeat("s", 32))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Lookup(context.Background(), p); err == nil {
				t.Fatal("substituted receipt accepted")
			}
		})
	}
}
