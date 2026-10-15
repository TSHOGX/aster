package evaluator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func TestHTTPProviderWorkerReconcilesLostSubmitResponse(t *testing.T) {
	worker, p, _ := fixture(t)
	secret := strings.Repeat("s", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != secret || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("X-Evaluation-Service-Digest") != p.service.Digest {
			t.Error("provider request authority mismatch")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "PUT":
			var submission evolution.EvaluationSubmission
			if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			_, _ = p.Submit(r.Context(), submission)
			// Real transport loses the accepted result, keeping provider state intact.
			w.WriteHeader(503)
		case strings.HasSuffix(r.URL.Path, "/result"):
			value, err := p.Result(r.Context(), "job")
			if err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(value)
		default:
			value, err := p.Status(r.Context(), "job")
			if errors.Is(err, ErrProviderJobNotFound) {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"evaluation_job_not_found"}`))
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(value)
		}
	}))
	defer server.Close()
	worker.Providers = HTTPRegistry{Catalog: worker.Store, Connections: map[string]Connection{"connection": {Endpoint: server.URL, ClientID: "evaluation-space", Secret: secret}}}
	if err := worker.Step(context.Background(), "group", "job"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("lost reply classification", err)
	}
	if err := worker.Step(context.Background(), "group", "job"); err != nil {
		t.Fatal(err)
	}
	result, err := worker.Store.EvaluationResult(context.Background(), "group", "job")
	if err != nil || result.Result.Outcome != "pass" || p.submits != 1 || calls != 4 {
		t.Fatal(result.Result.Outcome, p.submits, calls, err)
	}
}
func TestHTTPProviderRejectsAmbiguousAbsenceAndMalformedIdentity(t *testing.T) {
	_, p, _ := fixture(t)
	for _, mode := range []string{"missing", "plain404", "denied", "duplicate", "wrong-job", "oversized", "redirect", "server-error"} {
		t.Run(mode, func(t *testing.T) {
			redirected := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; w.WriteHeader(200) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "missing":
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"error":"evaluation_job_not_found"}`))
				case "plain404":
					http.NotFound(w, r)
				case "denied":
					w.WriteHeader(403)
				case "duplicate":
					_, _ = fmt.Fprint(w, `{"job_id":"job","job_id":"other","fence":1,"state":"completed"}`)
				case "wrong-job":
					_ = json.NewEncoder(w).Encode(evolution.JobStatus{JobID: "other", Fence: 1, State: "running"})
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", evolution.MaxDocumentBytes+1)))
				case "redirect":
					http.Redirect(w, r, target.URL, 302)
				case "server-error":
					w.WriteHeader(500)
					_, _ = w.Write([]byte("provider diagnostic with private details"))
				}
			}))
			defer server.Close()
			client, err := NewHTTPProvider(p.service, Connection{Endpoint: server.URL, ClientID: "client", Secret: strings.Repeat("s", 32)})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Status(context.Background(), "job")
			expected := ErrProviderUnavailable
			if mode == "missing" {
				expected = ErrProviderJobNotFound
			}
			if mode == "denied" {
				expected = ErrProviderDenied
			}
			if !errors.Is(err, expected) || redirected != 0 {
				t.Fatal(mode, err, redirected)
			}
		})
	}
}
func TestHTTPRegistryUsesRegisteredConnectionOnly(t *testing.T) {
	worker, p, _ := fixture(t)
	for _, endpoint := range []string{"http://remote.example", "http://localhost", "https://example.com/path", "https://user:pass@example.com", "https://example.com?x=1", "https://example.com#fragment"} {
		if _, err := NewHTTPProvider(p.service, Connection{Endpoint: endpoint, ClientID: "client", Secret: strings.Repeat("s", 32)}); err == nil {
			t.Fatal("unsafe provider origin", endpoint)
		}
	}
	registry := HTTPRegistry{Catalog: worker.Store, Connections: map[string]Connection{}}
	if _, err := registry.Resolve(context.Background(), p.service); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("unregistered connection resolved", err)
	}
}
