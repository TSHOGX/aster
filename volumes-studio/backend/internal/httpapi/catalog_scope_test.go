package httpapi

import (
	"aster/contracts/appidentity"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/identity"
)

type catalogAuthority struct {
	allowed     map[string]map[string]bool
	unavailable bool
}

func (a *catalogAuthority) Authorize(_ context.Context, request evolution.AccessRequest) (evolution.AccessDecision, error) {
	if a.unavailable {
		return evolution.AccessDecision{}, errors.New("authority unavailable")
	}
	return evolution.AccessDecision{Allowed: request.Action == "volume.read" && a.allowed[request.Principal.Subject][request.LibraryID], PolicyRevision: 1}, nil
}
func TestCatalogScopesMetadataCountsAndCursorContinuation(t *testing.T) {
	ctx := context.Background()
	authority := &catalogAuthority{allowed: map[string]map[string]bool{"alpha": {}, "beta": {}}}
	store, err := authoring.Open(ctx, authoring.Config{Directory: t.TempDir(), PublisherID: "fixture", QuotaBytes: 32 << 20, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, name := range []string{"Alpha first", "Alpha second", "Secret beta"} {
		library, err := store.Create(ctx, authoring.Actor{Issuer: "https://example.com", Subject: "admin"}, authoring.CreateInput{RequestID: strings.ReplaceAll(name, " ", "-"), Name: name})
		if err != nil {
			t.Fatal(err)
		}
		subject := "alpha"
		if strings.HasPrefix(name, "Secret") {
			subject = "beta"
		}
		authority.allowed[subject][library.ID] = true
	}
	api := NewAuthoringAPI(store)
	call := func(subject, path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest("GET", "https://example.com"+apiPrefix+path, nil)
		response := httptest.NewRecorder()
		api.Serve(identity.Session{Session: appidentity.Session{Issuer: "https://example.com", Subject: subject}}, response, request)
		return response
	}
	for _, subject := range []string{"alpha", "beta", "nobody"} {
		response := call(subject, "libraries")
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var list struct {
			Libraries []authoring.Library `json:"libraries"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		expected := 0
		if subject == "alpha" {
			expected = 2
		}
		if subject == "beta" {
			expected = 1
		}
		if len(list.Libraries) != expected {
			t.Fatal(subject, list)
		}
		for _, library := range list.Libraries {
			if !authority.allowed[subject][library.ID] {
				t.Fatal("foreign metadata", library)
			}
		}
	}
	query := "libraries?mode=workbench&limit=1&sort=name&direction=asc"
	response := call("alpha", query)
	var page authoring.Page[authoring.LibrarySummary]
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 2 || len(page.Items) != 1 || page.Next == "" || strings.Contains(response.Body.String(), "Secret beta") {
		t.Fatal(response.Code, response.Body.String())
	}
	next := query + "&cursor=" + url.QueryEscape(page.Next)
	response = call("alpha", next)
	if response.Code != 200 || strings.Contains(response.Body.String(), "Secret beta") {
		t.Fatal(response.Code, response.Body.String())
	}
	if response = call("beta", next); response.Code != 422 {
		t.Fatal("cursor crossed principal", response.Code, response.Body.String())
	}
	// A hidden library mutation does not invalidate the visible snapshot.
	if _, err = store.Create(ctx, authoring.Actor{Issuer: "https://example.com", Subject: "admin"}, authoring.CreateInput{RequestID: "hidden", Name: "Another secret"}); err != nil {
		t.Fatal(err)
	}
	if response = call("alpha", next); response.Code != 200 {
		t.Fatal("hidden data influenced cursor", response.Code, response.Body.String())
	}
	delete(authority.allowed["alpha"], page.Items[0].ID)
	if response = call("alpha", next); response.Code != 409 {
		t.Fatal("revoked cursor retained snapshot", response.Code, response.Body.String())
	}
	authority.unavailable = true
	for _, path := range []string{"libraries", query} {
		response = call("alpha", path)
		if response.Code != 500 || strings.Contains(response.Body.String(), "Alpha") {
			t.Fatal("authority outage leaked catalog", response.Code, response.Body.String())
		}
	}
}
