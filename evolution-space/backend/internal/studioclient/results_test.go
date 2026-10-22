package studioclient

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

func TestResultTransportDistinguishesDefinitiveAbsenceAndAuthenticates(t *testing.T) {
	secret := strings.Repeat("s", 32)
	mode := "missing"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "evolution-space" || password != secret || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
			t.Error("incorrect machine identity")
		}
		switch mode {
		case "missing":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case "gateway":
			http.NotFound(w, r)
		case "redirect":
			http.Redirect(w, r, "https://forbidden.example", 302)
		case "denied":
			w.WriteHeader(403)
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"state": "submitted"})
		}
	}))
	defer server.Close()
	client, err := New(server.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.resultCall(context.Background(), "lookup", evolution.RunResultScope{}); !errors.Is(err, evolution.ErrRunResultNotFound) {
		t.Fatal(err)
	}
	for _, value := range []string{"gateway", "redirect", "denied"} {
		mode = value
		if _, err = client.resultCall(context.Background(), "lookup", evolution.RunResultScope{}); err == nil || errors.Is(err, evolution.ErrRunResultNotFound) {
			t.Fatal("ambiguous absence permits submit", value, err)
		}
	}
}
