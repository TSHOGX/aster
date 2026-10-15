package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunAdmissionExactScopeAndFailClosedResponse(t *testing.T) {
	secret := strings.Repeat("s", 32)
	scope := RunAdmissionScope{Subject: "actor", GroupID: "group", BindingID: "binding", Revision: 2, Digest: "sha256:" + strings.Repeat("a", 64), Automatic: true}
	for _, test := range []struct {
		body     string
		status   int
		expected error
	}{{`{"allowed":true}`, 200, nil}, {`{"allowed":false}`, 200, ErrRunAdmissionDenied}, {`{"allowed":true,"allowed":false}`, 200, ErrRunAuthorityUnavailable}, {`{"allowed":true}`, 503, ErrRunAuthorityUnavailable}, {`{}`, 403, ErrRunAdmissionDenied}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if !ok || user != "evaluation-space" || password != secret {
				t.Error("missing service identity")
			}
			if r.URL.Path != "/api/identity/v1/run-admission-authority" {
				t.Error("wrong authority route")
			}
			var received RunAdmissionScope
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil || received != scope {
				t.Error("scope changed", received, err)
			}
			w.WriteHeader(test.status)
			w.Write([]byte(test.body))
		}))
		authority, err := NewProjectionAuthority(ProjectionConfig{Endpoint: server.URL, ClientID: "evaluation-space", ClientSecret: secret, CentralSecret: secret})
		if err != nil {
			t.Fatal(err)
		}
		err = authority.AuthorizeRunAdmission(context.Background(), scope)
		server.Close()
		if !errors.Is(err, test.expected) {
			t.Fatalf("got %v want %v", err, test.expected)
		}
	}
}
