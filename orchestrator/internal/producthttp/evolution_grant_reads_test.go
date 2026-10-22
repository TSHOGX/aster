package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrantReadHTTPRequiresCurrentAdministrator(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "grants.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: "member", Username: "member", DisplayName: "Member", Role: "user"})
	must(t, err)
	_, temporary, err := store.Authenticate(ctx, "member", created.TemporaryPassword)
	must(t, err)
	_, member, err := store.ChangePassword(ctx, temporary, "", "permanent-member-password", true)
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	call := func(credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/grants/"+admin.ID, nil)
		if credential != "" {
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		studioRequest := httptest.NewRequest("GET", "https://app/api/admin/studio-grants?kind=account&subject_id="+admin.ID+"&library_id=library", nil)
		if credential != "" {
			studioRequest.AddCookie(&http.Cookie{Name: loginCookie, Value: credential})
		}
		studio := httptest.NewRecorder()
		handler.ServeHTTP(studio, studioRequest)
		if studio.Code != w.Code || studio.Code == 200 && !strings.Contains(studio.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("Studio policy boundary differs", studio.Code, studio.Body.String())
		}
		return w
	}
	if w := call(""); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(member); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(token); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal(w.Code, w.Body.String())
	}
	_, err = store.SetEvolutionGroupGrant(ctx, token, product.StandardUsersGroup, product.EvolutionGroupGrantInput{RequestID: "deny-config", AccountID: admin.ID, Action: "config.read", Effect: "deny"})
	must(t, err)
	if w := call(token); w.Code != 200 {
		t.Fatal("cannot repair denied config", w.Code, w.Body.String())
	}
}
