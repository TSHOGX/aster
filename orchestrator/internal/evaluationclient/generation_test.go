package evaluationclient

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerationTransportExactInputAndAdmissionReceipts(t *testing.T) {
	secret := strings.Repeat("s", 32)
	body := []byte(`{"episodes":[]}`)
	ref := evolution.ArtifactRef{ID: "input", GroupID: "group", Digest: evolution.Digest(body), SchemaDigest: evolution.Digest(evolution.CorpusSchema), Size: int64(len(body)), MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	a := evolution.GenerationAssignment{RunID: "run", BatchID: "batch", GroupID: "group", BindingID: "binding", OccurrenceID: "event", Requester: evolution.Principal{Issuer: "https://app", Subject: "actor", Kind: "human"}, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, MaxArtifactBytes: 1024, TimeoutSeconds: 60}, Pins: evolution.RunPins{Inputs: []evolution.ArtifactRef{ref}, InputPorts: map[string]evolution.ArtifactRef{"corpus": ref}, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "skills", Profile: "authored-v1", ReservationGeneration: 1}}}
	corrupt := false
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "central" || password != secret || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("service identity missing")
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		if strings.HasSuffix(r.URL.Path, "seal-input") {
			raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Corpus-Artifact"))
			if err != nil {
				t.Error(err)
			}
			var submitted evolution.ArtifactRef
			if err = json.Unmarshal(raw, &submitted); err != nil || submitted != ref {
				t.Error("artifact header mismatch", err)
			}
			data, _ := io.ReadAll(r.Body)
			if !bytes.Equal(data, body) || r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Error("artifact body mismatch")
			}
			if corrupt {
				submitted.Digest = evolution.Digest(nil)
			}
			_ = json.NewEncoder(w).Encode(submitted)
			return
		}
		var submitted evolution.GenerationAssignment
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			t.Error(err)
		}
		result := evolution.Run{ID: submitted.RunID, BatchID: submitted.BatchID, GroupID: submitted.GroupID, BindingID: submitted.BindingID, Pins: submitted.Pins, Budget: submitted.Budget}
		if corrupt {
			result.Pins.Base.CommitID = strings.Repeat("b", 64)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	client, err := New(server.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.SealGenerationInput(context.Background(), ref, body); err != nil {
		t.Fatal(err)
	}
	if _, err = client.AdmitGeneration(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	corrupt = true
	if err = client.SealGenerationInput(context.Background(), ref, body); err == nil {
		t.Fatal("substituted artifact accepted")
	}
	if _, err = client.AdmitGeneration(context.Background(), a); err == nil {
		t.Fatal("substituted pins accepted")
	}
	corrupt = false
	status = 503
	if _, err = client.AdmitGeneration(context.Background(), a); err == nil {
		t.Fatal("uncertain response accepted")
	}
}
