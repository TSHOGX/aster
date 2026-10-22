package httpapi

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"volume-studio/backend/internal/authoring"
)

type outputReaderFixture struct {
	scope           evolution.OutputCatalogScope
	reads           int
	revokeAfterRead bool
}

func (f *outputReaderFixture) ResolveEvolutionOutput(_ context.Context, scope evolution.OutputCatalogScope) (evolution.Principal, error) {
	f.reads++
	if scope != f.scope || f.revokeAfterRead && f.reads > 1 {
		return evolution.Principal{}, errors.New("denied")
	}
	return evolution.Principal{Issuer: "https://central", Subject: "manager", Kind: "human"}, nil
}
func TestPrivateOutputCatalogRequiresServiceAndExactLiveReader(t *testing.T) {
	store, err := authoring.Open(context.Background(), authoring.Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 64 << 20, Authority: &scopedTestAuthority{true}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scope := evolution.OutputCatalogScope{Ticket: "ticket", Query: evolution.OutputCatalogQuery{GroupID: "group", Limit: 25}}
	reader := &outputReaderFixture{scope: scope}
	secret := strings.Repeat("s", 32)
	api, err := NewEvolutionOutputAPI(store, reader, secret)
	if err != nil {
		t.Fatal(err)
	}
	call := func(cookie, origin, password string, scope evolution.OutputCatalogScope) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(scope)
		r := httptest.NewRequest("POST", "/volumes-studio/internal/v1/evolution/outputs", bytes.NewReader(raw))
		r.SetBasicAuth("central", password)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", cookie)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		return w
	}
	for _, args := range [][3]string{{"", "", "wrong"}, {"session=browser", "", secret}, {"", "https://browser", secret}} {
		if w := call(args[0], args[1], args[2], scope); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if reader.reads != 0 {
		t.Fatal("browser borrowed service authority")
	}
	if w := call("", "", secret, scope); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	reader.reads = 0
	reader.revokeAfterRead = true
	if w := call("", "", secret, scope); w.Code != 403 {
		t.Fatal("late read escaped revoke", w.Code)
	}
}
