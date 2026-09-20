// Command model-gateway terminates steered guest model traffic.
//
// It runs as its own process, under its own UID, because it is the only component
// that holds upstream model credentials. The application authorizes every request
// over a protected Unix socket and never sees those credentials; this process
// never decides authority for itself.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/gateway"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("model gateway stopped", "error", err)
		os.Exit(1)
	}
}

// gatewayFile is the gateway's own configuration. Credentials live here, in a
// file only this UID can read, and never in the product deployment document or
// in anything a guest can reach.
type gatewayFile struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	BackendID string `json:"backend_id"`
	// Listen is where steered guest traffic arrives. Reserved is the product's
	// synthetic destination range; a connection outside it is not a service.
	Listen   string `json:"listen"`
	Reserved string `json:"reserved"`
	// Control is this gateway's Unix control listener, which the application uses
	// to activate and revoke authority. ApplicationUID is the only accepted peer.
	Control        string  `json:"control"`
	ApplicationUID *uint32 `json:"application_uid"`
	ControlGroup   *int    `json:"control_group"`
	// Authorization is the application's trusted authorization socket.
	Authorization  string `json:"authorization"`
	RequestTimeout string `json:"request_timeout"`
	Upstreams      []struct {
		EndpointID   string            `json:"endpoint_id"`
		Models       []string          `json:"models"`
		BaseURL      string            `json:"base_url"`
		APIKeyHeader string            `json:"api_key_header"`
		APIKeyFile   string            `json:"api_key_file"`
		ExtraHeaders map[string]string `json:"extra_headers"`
	} `json:"upstreams"`
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("model-gateway", flag.ContinueOnError)
	path := flags.String("config", "", "gateway configuration document")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*path) {
		return fmt.Errorf("absolute --config path required")
	}
	wire, base, err := loadGatewayFile(*path)
	if err != nil {
		return err
	}
	timeout, err := time.ParseDuration(wire.RequestTimeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("positive request_timeout required")
	}
	reserved, err := netip.ParsePrefix(wire.Reserved)
	if err != nil {
		return fmt.Errorf("reserved must be the product's synthetic IPv4 range")
	}
	var upstreams []gateway.ModelUpstream
	for _, upstream := range wire.Upstreams {
		resolved := gateway.ModelUpstream{EndpointID: upstream.EndpointID, Models: upstream.Models, BaseURL: upstream.BaseURL, APIKeyHeader: upstream.APIKeyHeader, ExtraHeaders: upstream.ExtraHeaders}
		if upstream.APIKeyFile != "" {
			// The credential is read from its own file so it never appears in this
			// document, in a process argument or in an environment variable.
			key, err := readCredential(resolve(base, upstream.APIKeyFile))
			if err != nil {
				return fmt.Errorf("read upstream %q credential: %w", upstream.EndpointID, err)
			}
			resolved.APIKey = key
		}
		upstreams = append(upstreams, resolved)
	}
	flows := gateway.NewFlows()
	authorizer, err := newAuthorizationClient(resolve(base, wire.Authorization), timeout)
	if err != nil {
		return err
	}
	handler, err := gateway.NewModelGateway(gateway.ModelOptions{
		BackendID: wire.BackendID, Reserved: reserved, Upstreams: upstreams,
		Flows: flows, Authorizer: authorizer, RequestTimeout: timeout,
	})
	if err != nil {
		return err
	}
	control, err := gateway.ControlHandler(flows, gateway.ControlOptions{ID: wire.ID, ApplicationUID: *wire.ApplicationUID, MaxOperationTimeout: timeout})
	if err != nil {
		return err
	}
	controlListener, err := gateway.ListenControl(resolve(base, wire.Control), *wire.ControlGroup)
	if err != nil {
		return err
	}
	defer controlListener.Close()
	trafficListener, err := net.Listen("tcp4", wire.Listen)
	if err != nil {
		return err
	}
	defer trafficListener.Close()
	// The traffic server has no write timeout: a streaming generation holds one
	// response open for as long as the upstream is producing tokens, and the grant
	// deadline is the authority clock. Read bounds still defend against slow sends.
	traffic := &http.Server{
		Handler: handler, ConnContext: gateway.DestinationContext(reserved),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: timeout, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
	}
	controlServer := &http.Server{
		Handler: control, ConnContext: gateway.PeerContext,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: timeout, WriteTimeout: timeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192,
	}
	slog.Info("model gateway listening", "id", wire.ID, "listen", trafficListener.Addr().String(), "control", wire.Control)
	results := make(chan error, 2)
	go func() { results <- traffic.Serve(trafficListener) }()
	go func() { results <- controlServer.Serve(controlListener) }()
	<-ctx.Done()
	// Drain before closing the listeners: shutdown must mean upstream work has
	// stopped, not merely that no new connection is accepted.
	drain, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	drainErr := flows.Close(drain)
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopShutdown()
	closeErr := errors.Join(traffic.Shutdown(shutdown), controlServer.Shutdown(shutdown))
	first, second := <-results, <-results
	for _, err := range []error{first, second} {
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return errors.Join(drainErr, closeErr)
}

func loadGatewayFile(path string) (gatewayFile, string, error) {
	var wire gatewayFile
	file, err := os.Open(path)
	if err != nil {
		return wire, "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return wire, "", err
	}
	if len(raw) > 1<<20 {
		return wire, "", fmt.Errorf("gateway configuration exceeds 1 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return wire, "", fmt.Errorf("gateway configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return wire, "", fmt.Errorf("gateway configuration contains trailing data")
	}
	if wire.Version != 1 || wire.ID == "" || wire.BackendID == "" || wire.Listen == "" || wire.Control == "" || wire.Authorization == "" || wire.ApplicationUID == nil || wire.ControlGroup == nil || *wire.ControlGroup < 0 || len(wire.Upstreams) == 0 {
		return wire, "", fmt.Errorf("version 1, identities, listeners, application peer UID and upstreams required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return wire, "", err
	}
	return wire, filepath.Dir(absolute), nil
}

func resolve(base, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(base, value)
}

// readCredential refuses a credential file other users can read. A secret that is
// world-readable is not isolated from the application or the guest's UID.
func readCredential(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("credential must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential file must not be group or world accessible")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("credential file is empty")
	}
	return value, nil
}

// authorizationClient reaches the application's trusted contract over its
// protected Unix socket. It never falls back to a network endpoint.
type authorizationClient struct {
	client *http.Client
}

func newAuthorizationClient(socket string, timeout time.Duration) (*authorizationClient, error) {
	if !filepath.IsAbs(socket) {
		return nil, fmt.Errorf("absolute application authorization socket required")
	}
	transport := &http.Transport{
		MaxIdleConns: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second,
		ResponseHeaderTimeout: timeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &authorizationClient{client: &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (a *authorizationClient) Authorize(ctx context.Context, request egress.AuthorizationRequest) (egress.Authorization, error) {
	var result egress.Authorization
	status, body, err := a.post(ctx, "/v1/authorize", request)
	if err != nil {
		return result, err
	}
	switch status {
	case http.StatusOK:
		return result, json.Unmarshal(body, &result)
	case http.StatusForbidden:
		return result, egress.ErrDenied
	case http.StatusConflict:
		return result, egress.ErrRequestConflict
	}
	return result, fmt.Errorf("authorization unavailable: HTTP %d", status)
}

func (a *authorizationClient) Finish(ctx context.Context, request egress.RequestFinish) error {
	status, _, err := a.post(ctx, "/v1/finish", request)
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		return nil
	}
	if status == http.StatusConflict {
		return egress.ErrRequestConflict
	}
	return fmt.Errorf("request completion unavailable: HTTP %d", status)
}

func (a *authorizationClient) post(ctx context.Context, path string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://application"+path, strings.NewReader(string(raw)))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return response.StatusCode, body, err
}
