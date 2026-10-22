package evolutionspaceclient

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogVerifiesImmutableDefinitionsAndOmitsServiceWiring(t *testing.T) {
	service := evolution.Service{Ref: evolution.Revision{ID: "native", Revision: 1}, Kind: "evolution", ConnectionID: "private-connection", RunnerDigest: evolution.Digest(nil), CredentialRef: "private-credential", CredentialGeneration: 4, Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	corrupt := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "central" || secret != strings.Repeat("s", 32) || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("invalid service boundary")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(CatalogServiceStatus{ID: "native", Revision: 2, Enabled: false})
		case strings.HasSuffix(r.URL.Path, "/1"):
			value := service
			if corrupt {
				value.Kind = "evaluation"
			}
			_ = json.NewEncoder(w).Encode(value)
		default:
			_ = json.NewEncoder(w).Encode(CatalogPage{Revisions: []evolution.Revision{service.Ref}, NextID: "native", NextRevision: 1})
		}
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Catalog(context.Background(), "service", "", 0, 25)
	if err != nil || len(page.Revisions) != 1 {
		t.Fatal(page, err)
	}
	entry, err := client.CatalogDefinition(context.Background(), "service", service.Ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(entry)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "credential") || entry.Status.Enabled {
		t.Fatal("operator wiring leaked or revoked service advertised enabled", string(raw))
	}
	corrupt = true
	if _, err = client.CatalogDefinition(context.Background(), "service", service.Ref); err == nil {
		t.Fatal("substituted catalog content accepted")
	}
	if _, err = client.Catalog(context.Background(), "../escape", "", 0, 25); err == nil {
		t.Fatal("unknown catalog kind accepted")
	}
	if _, err = client.Catalog(context.Background(), "service", "", 1, 25); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestCatalogValidationBindsExactConfiguration(t *testing.T) {
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	digest := evolution.Digest(raw)
	wrong := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var submitted evolution.Configuration
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			t.Error(err)
		}
		identity := digest
		if wrong {
			identity = evolution.Digest(nil)
		}
		_ = json.NewEncoder(w).Encode(ConfigurationValidation{Digest: identity, Valid: true})
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ValidateConfiguration(context.Background(), config)
	if err != nil || !result.Valid {
		t.Fatal(result, err)
	}
	wrong = true
	if _, err = client.ValidateConfiguration(context.Background(), config); err == nil {
		t.Fatal("substituted preview identity accepted")
	}
}
