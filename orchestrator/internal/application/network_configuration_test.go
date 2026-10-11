package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/producttest"
)

func TestNetworkConfigurationIsDatabaseOwnedAndReloadRouteIsAbsent(t *testing.T) {
	store, err := producttest.Open(context.Background(), filepath.Join(t.TempDir(), "network.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, path := configurationFixture(t)
	app := &Application{Store: store, Config: cfg}
	before, err := app.networkReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Project.Reachability = product.Reachability{}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := app.networkReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Defaults.Revision != after.Defaults.Revision {
		t.Fatal("configuration file overwrote database network")
	}
	response := httptest.NewRecorder()
	app.controlHandler().ServeHTTP(response, httptest.NewRequest("POST", "http://owner/network/reload", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("retired network reload route remains", response.Code)
	}
}
