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

func TestOutputCatalogTransportVerifiesIdentityAndHidesForeignBindings(t *testing.T) {
	scope := evolution.OutputCatalogScope{Ticket: "ticket", Query: evolution.OutputCatalogQuery{GroupID: "group", LibraryID: "library", Limit: 25}}
	detail := evolution.OutputLibraryDetail{Library: evolution.OutputLibrary{ID: "library", Profile: "authored-v1"}, Main: evolution.ContentRef{LibraryID: "library", Profile: "authored-v1", CommitID: strings.Repeat("a", 64), TreeDigest: evolution.Digest(nil)}, Reservations: []evolution.OutputReservation{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "central" || secret != strings.Repeat("s", 32) || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("invalid private transport")
		}
		var received evolution.OutputCatalogScope
		if json.NewDecoder(r.Body).Decode(&received) != nil || received != scope {
			t.Error("scope changed")
		}
		_ = json.NewEncoder(w).Encode(detail)
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.OutputLibrary(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	detail.Reservations = []evolution.OutputReservation{{Prefix: "skills", GroupID: "other", BindingID: "secret-binding", Generation: 1, State: "active"}}
	if _, err = client.OutputLibrary(context.Background(), scope); err == nil {
		t.Fatal("foreign binding metadata accepted")
	}
	detail.Reservations = nil
	detail.Main.LibraryID = "other"
	if _, err = client.OutputLibrary(context.Background(), scope); err == nil {
		t.Fatal("substituted main accepted")
	}
}
