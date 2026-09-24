package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
)

func TestCompletedOwnerHandoffRetryDoesNotDependOnCurrentGateway(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "product.db")
	identity := product.ApplicationIdentity{ExecutableDigest: "sha256:" + strings.Repeat("a", 64), CompatibilityDigest: "sha256:" + strings.Repeat("c", 64)}
	store, err := product.OpenApplication(ctx, path, identity)
	if err != nil {
		t.Fatal(err)
	}
	target := "sha256:" + strings.Repeat("b", 64)
	proof := func() product.GatewayObservation { return product.GatewayObservation{Started: time.Now()} }
	drain, err := store.BeginOwnerHandoff(ctx, "retry-owner", time.Minute, target, identity.CompatibilityDigest, proof())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.TryReleaseReady(ctx, drain.Owner, drain.Generation, proof()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ActivateRelease(ctx, drain.Owner, drain.Generation, target, proof()); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	identity.ExecutableDigest = target
	store, err = product.OpenApplication(ctx, path, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.FinishRelease(ctx, drain.Owner, drain.Generation, target, proof()); err != nil {
		t.Fatal(err)
	}
	// No gateway listener exists now. The durable completed result is still the
	// authoritative response to a lost completion acknowledgment.
	app := &Application{Store: store, Config: Config{StateDirectory: t.TempDir(), Gateways: []ServiceGateway{{Audience: "model", MaxRequests: 1}}}, releaseIdentity: ReleaseIdentity{ExecutableDigest: target}}
	response := httptest.NewRecorder()
	app.controlHandler().ServeHTTP(response, httptest.NewRequest("POST", "/release/complete", strings.NewReader(`{"owner":"retry-owner","generation":1}`)))
	if response.Code != 200 {
		t.Fatal("completed retry required a new live gateway observation", response.Code, response.Body.String())
	}
}

func TestGatewayHandoffObservation(t *testing.T) {
	for _, test := range []struct {
		name              string
		capability, slots int
		stopping, wantOK  bool
	}{
		{"old gateway", 0, 1, false, false},
		{"compatible", product.OwnerHandoffProtocol, 1, false, true},
		{"capacity changed", product.OwnerHandoffProtocol, 2, false, false},
		{"stopping", product.OwnerHandoffProtocol, 1, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &Application{Config: Config{StateDirectory: t.TempDir(), Gateways: []ServiceGateway{{Audience: "model", MaxRequests: 1}}}}
			listener, err := listenControl(app.Config.GatewaySocket())
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"authority_protocol": 1, "owner_handoff_protocol": test.capability, "stopping": test.stopping, "registrations": map[string]product.GatewayRegistration{"model": {Instance: "gateway", Generation: 1, Slots: test.slots}}})
			})}
			go server.Serve(listener)
			defer server.Close()
			proof, err := app.observeGateways(context.Background())
			if (err == nil) != test.wantOK {
				t.Fatal(test.name, proof, err)
			}
		})
	}
}
