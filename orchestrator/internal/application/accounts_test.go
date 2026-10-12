package application

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestOfflineBootstrapAndProtectedOwnerRecovery(t *testing.T) {
	ctx := context.Background()
	declaration, path := configurationFixture(t)
	raw, err := json.Marshal(declaration)
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
	account, err := AccountOperation(ctx, cfg, "bootstrap", "Administrator", "permanent-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AccountOperation(ctx, cfg, "bootstrap", "another", "another-admin-password"); err == nil {
		t.Fatal("second bootstrap accepted")
	}
	store, err := product.Open(ctx, cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = AccountOperation(ctx, cfg, "recover-admin", "administrator", "temporary-admin-password"); err == nil {
		t.Fatal("offline recovery opened a second writer")
	}
	app := &Application{Store: store}
	server := httptest.NewUnstartedServer(app.controlHandler())
	listener, err := listenControl(filepath.Join(cfg.StateDirectory, "operator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	_, old, err := store.Authenticate(ctx, account.Username, "permanent-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = Control(ctx, listener.Addr().String(), "POST", "/account/recover-admin", map[string]string{"username": "administrator", "password": "temporary-admin-password"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "password") {
		t.Fatal("operator response leaked credentials")
	}
	if _, err = store.Login(ctx, old); err == nil {
		t.Fatal("recovery retained old cookie")
	}
	session, _, err := store.Authenticate(ctx, "administrator", "temporary-admin-password")
	if err != nil || !session.Restricted || session.Account.ID != account.ID {
		t.Fatal(session, err)
	}
}
