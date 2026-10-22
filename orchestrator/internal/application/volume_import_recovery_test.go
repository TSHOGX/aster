package application

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	manifest "aster/contracts/volumemanifest"
	"harness-platform/orchestrator/internal/product"
)

func TestImportHTTPRetryReplaysAfterStateTransition(t *testing.T) {
	ctx := context.Background()
	config, path := configurationFixture(t)
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AccountOperation(ctx, cfg, "bootstrap", "import-admin", "permanent-admin-password"); err != nil {
		t.Fatal(err)
	}
	store, err := product.Open(ctx, cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, token, err := store.Authenticate(ctx, "import-admin", "permanent-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := manifest.New("publisher", "library", "version", []manifest.Entry{{Path: "empty", Kind: "directory", Size: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = descriptor.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := descriptor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := store.AdmitVolumeImport(ctx, token, product.VolumeImportInput{RequestID: "admit", SourceID: "studio", VersionID: "version", ExpectedManifestDigest: digest, ConsumerProfile: manifest.Profile}, raw, "engine", "publisher", 1<<30, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceVolumeImport(ctx, imported.ID, "acquiring", "failed", "interrupted"); err != nil {
		t.Fatal(err)
	}
	cfg.VolumeImports = &VolumeImportConfig{EngineBytes: 1 << 30}
	app := &Application{Store: store, Config: cfg}
	handler := app.volumeImportHandler()
	call := func(body string, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/admin/volume-imports/"+imported.ID+"/retry", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "project_login", Value: cookie})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	original := `{"request_id":"retry"}`
	for attempt := 0; attempt < 2; attempt++ {
		w := call(original, token)
		if w.Code != 200 {
			t.Fatalf("retry %d: %d %s", attempt, w.Code, w.Body.String())
		}
	}
	// Once admitted, replay does not need a still-configured source or budget.
	// Durable receipt remains usable after database reopen.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := product.Open(ctx, cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	app.Store = reopened
	app.Config.VolumeImports = nil
	w := call(original, token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(original, "invalid")
	if w.Code != 401 {
		t.Fatal("receipt bypassed authority", w.Code)
	}
	w = call(`{"request_id":"new-attempt"}`, token)
	if w.Code == 200 {
		t.Fatal("new retry bypassed current state")
	}
}
