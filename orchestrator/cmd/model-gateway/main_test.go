package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

func writeGatewayFile(t *testing.T, value map[string]any) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "key"), []byte("upstream-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gateway.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func gatewayValue() map[string]any {
	uid := os.Getuid()
	return map[string]any{
		"version": 1, "id": "model", "backend_id": "engine-store",
		"listen": "127.0.0.1:0", "reserved": "10.80.0.0/24",
		"control": "control.sock", "application_uid": uid, "control_group": os.Getgid(),
		"authorization": "/run/harness/authorization.sock", "request_timeout": "5s",
		"upstreams": []any{map[string]any{"reference": "models", "base_url": "https://upstream.example", "api_key_header": "X-Api-Key", "api_key_file": "key"}},
	}
}

func TestGatewayConfigurationRequiresCompleteIdentityAndCredentials(t *testing.T) {
	path := writeGatewayFile(t, gatewayValue())
	wire, base, err := loadGatewayFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if wire.ID != "model" || wire.Upstreams[0].APIKeyFile != "key" || base != filepath.Dir(path) {
		t.Fatalf("configuration: %+v", wire)
	}
	// The credential is read from its own file, so it never appears in the
	// document, a process argument or an environment variable.
	key, err := readCredential(resolve(base, wire.Upstreams[0].APIKeyFile))
	if err != nil || key != "upstream-secret" {
		t.Fatalf("credential: %q %v", key, err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"version":        func(v map[string]any) { v["version"] = 2 },
		"no id":          func(v map[string]any) { v["id"] = "" },
		"no backend":     func(v map[string]any) { v["backend_id"] = "" },
		"no listen":      func(v map[string]any) { v["listen"] = "" },
		"no control":     func(v map[string]any) { v["control"] = "" },
		"no peer":        func(v map[string]any) { delete(v, "application_uid") },
		"no group":       func(v map[string]any) { delete(v, "control_group") },
		"negative group": func(v map[string]any) { v["control_group"] = -1 },
		"no upstreams":   func(v map[string]any) { v["upstreams"] = []any{} },
		"unknown field":  func(v map[string]any) { v["extra"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			value := gatewayValue()
			mutate(value)
			if _, _, err := loadGatewayFile(writeGatewayFile(t, value)); err == nil {
				t.Fatalf("invalid configuration accepted: %s", name)
			}
		})
	}
}

// A credential other users can read is not isolated from the application or the
// guest's UID, so loading one must fail rather than warn.
func TestCredentialMustNotBeGroupOrWorldAccessible(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "key")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(path); err == nil {
		t.Fatal("group-readable credential accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(path); err != nil {
		t.Fatalf("owner-only credential rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(path); err == nil {
		t.Fatal("empty credential accepted")
	}
	if _, err := readCredential(filepath.Join(root, "absent")); err == nil {
		t.Fatal("missing credential accepted")
	}
	if err := os.Symlink(path, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(filepath.Join(root, "link")); err == nil {
		t.Fatal("symlinked credential accepted")
	}
}

// The authorization client speaks only to its Unix socket and maps the contract's
// statuses onto typed outcomes, so the adapter fails closed on every one.
func TestAuthorizationClientMapsContractStatuses(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "authorization.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var status int
	var body string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/finish" {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	client, err := newAuthorizationClient(socket, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	status, body = http.StatusOK, `{"service_id":"model","grant_id":"grant"}`
	authorization, err := client.Authorize(ctx, egress.AuthorizationRequest{RequestID: "one"})
	if err != nil || authorization.GrantID != "grant" || authorization.ServiceID != "model" {
		t.Fatalf("authorized: %+v %v", authorization, err)
	}
	status, body = http.StatusForbidden, ""
	if _, err := client.Authorize(ctx, egress.AuthorizationRequest{RequestID: "two"}); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("denial: %v", err)
	}
	status = http.StatusConflict
	if _, err := client.Authorize(ctx, egress.AuthorizationRequest{RequestID: "three"}); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatalf("conflict: %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := client.Authorize(ctx, egress.AuthorizationRequest{RequestID: "four"}); err == nil {
		t.Fatal("unavailable authorization must fail closed")
	}
	status = http.StatusNoContent
	if err := client.Finish(ctx, egress.RequestFinish{RequestID: "one", Outcome: "ok"}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	status = http.StatusConflict
	if err := client.Finish(ctx, egress.RequestFinish{RequestID: "one", Outcome: "other"}); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatalf("contradictory finish: %v", err)
	}
	if _, err := newAuthorizationClient("relative.sock", time.Second); err == nil {
		t.Fatal("relative authorization socket accepted")
	}
}
