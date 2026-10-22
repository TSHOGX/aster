package appidentity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestApplicationsRejectForeignAuthority(t *testing.T) {
	if _, ok := ApplicationFor("evaluation-space"); ok {
		t.Fatal("retired application identity accepted")
	}
	for _, id := range []string{"studio", "evolution-space"} {
		t.Run(id, func(t *testing.T) {
			app, _ := ApplicationFor(id)
			otherID := "studio"
			if id == otherID {
				otherID = "evolution-space"
			}
			other, _ := ApplicationFor(otherID)
			for _, scenario := range []string{"valid", "foreign_entitlement", "foreign_issuer", "expired", "malformed", "outage", "redirect"} {
				t.Run(scenario, func(t *testing.T) {
					central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						user, password, ok := r.BasicAuth()
						if !ok || user != id || password != strings.Repeat("s", 32) {
							t.Error("wrong application credential")
						}
						var body map[string]string
						if json.NewDecoder(r.Body).Decode(&body) != nil || body["redirect_uri"] != "https://app"+app.Prefix+"auth/callback" {
							t.Error("wrong application callback")
						}
						a := Assertion{Issuer: "https://app", Subject: "member", CentralSession: "central", Epoch: 1, Entitlements: []string{app.AccessEntitlement}, ExpiresAt: time.Now().Add(time.Hour), Grant: strings.Repeat("g", 43)}
						switch scenario {
						case "foreign_entitlement":
							a.Entitlements = []string{other.AccessEntitlement}
						case "foreign_issuer":
							a.Issuer = "https://foreign"
						case "expired":
							a.ExpiresAt = time.Now().Add(-time.Minute)
						case "malformed":
							_, _ = w.Write([]byte(`{"unknown":true}`))
							return
						case "outage":
							w.WriteHeader(503)
							return
						case "redirect":
							w.Header().Set("Location", "https://untrusted.invalid")
							w.WriteHeader(307)
							return
						}
						_ = json.NewEncoder(w).Encode(a)
					}))
					defer central.Close()
					client, err := NewClient(Config{Issuer: "https://app", Endpoint: central.URL, ClientID: id, ClientSecret: strings.Repeat("s", 32)})
					if err != nil {
						t.Fatal(err)
					}
					started := time.Now()
					assertion, err := client.Exchange(context.Background(), strings.Repeat("c", 43), strings.Repeat("v", 43))
					switch scenario {
					case "valid":
						if err != nil || assertion.ExpiresAt.After(time.Now().Add(time.Minute)) || assertion.ExpiresAt.Before(started) {
							t.Fatalf("valid bounded authority: %+v %v", assertion, err)
						}
					case "foreign_entitlement", "foreign_issuer", "expired":
						if !errors.Is(err, ErrUnauthorized) {
							t.Fatalf("got %v", err)
						}
					default:
						if !errors.Is(err, ErrUnavailable) {
							t.Fatalf("got %v", err)
						}
					}
					authorize, _ := url.Parse(client.AuthorizeURL("state", "challenge"))
					if authorize.Query().Get("client_id") != id || authorize.Query().Get("redirect_uri") != "https://app"+app.Prefix+"auth/callback" {
						t.Fatal("authorization crosses application")
					}
				})
			}
			if app.SessionCookie() == other.SessionCookie() || app.LoginCookie() == other.LoginCookie() {
				t.Fatal("application cookies collide")
			}
			for _, value := range []string{other.Prefix, "//foreign/path", app.Prefix + "%2e%2e/", app.Prefix + "auth/callback", app.Prefix + "%5cforeign", app.Prefix + "x#fragment"} {
				if _, err := LocalReturn(app.Prefix, value); !errors.Is(err, ErrUnauthorized) {
					t.Fatalf("accepted unsafe return %q", value)
				}
			}
			if got, err := LocalReturn(app.Prefix, app.Prefix+"groups/team?tab=runs"); err != nil || got != app.Prefix+"groups/team?tab=runs" {
				t.Fatalf("lost deep link: %s %v", got, err)
			}
		})
	}
}
