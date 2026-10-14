package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func TestScopedAuthorityFailsClosed(t *testing.T) {
	for _, fixture := range []struct {
		name, body         string
		status             int
		allowed, wantError bool
	}{
		{"allow", `{"allowed":true,"policy_revision":1,"reason":"allow"}`, 200, true, false},
		{"deny", `{"allowed":false,"policy_revision":2,"reason":"deny"}`, 200, false, false},
		{"missing_revision", `{"allowed":true,"reason":"allow"}`, 200, false, true},
		{"zero_revision", `{"allowed":true,"policy_revision":0,"reason":"allow"}`, 200, false, true},
		{"duplicate", `{"allowed":false,"allowed":true,"policy_revision":1,"reason":"allow"}`, 200, false, true},
		{"outage", `{}`, 503, false, true},
		{"redirect", `{}`, 302, false, true},
		{"unauthorized", `{}`, 401, false, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "studio" || password != strings.Repeat("secret", 8) {
					t.Error("service credentials missing")
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
					t.Error("ambient credentials")
				}
				w.WriteHeader(fixture.status)
				_, _ = w.Write([]byte(fixture.body))
			}))
			defer server.Close()
			client, err := NewClient(Config{Issuer: "https://example.com", Endpoint: server.URL, ClientID: "studio", ClientSecret: strings.Repeat("secret", 8)})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := client.Authorize(context.Background(), evolution.AccessRequest{Principal: evolution.Principal{Issuer: "https://example.com", Subject: "expert", Kind: "human"}, LibraryID: "library", Action: "volume.read"})
			if (err != nil) != fixture.wantError || decision.Allowed != fixture.allowed {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
		})
	}
}
