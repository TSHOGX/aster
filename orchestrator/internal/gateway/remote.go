package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

var ErrGatewayRestarted = errors.New("gateway instance changed; reconciliation required")

type RemoteConfig struct {
	ID, Socket string
	Protocol   egress.Protocol
	PeerUID    uint32
	Timeout    time.Duration
}

// Remote uses a kernel-authenticated Unix peer in both directions. It never
// follows redirects or uses HTTP proxy environment variables. The instance pin
// survives operation retries; replacing it requires explicit reconstruction.
type Remote struct {
	config    RemoteConfig
	info      ControlInfo
	client    *http.Client
	transport *http.Transport
}

func NewRemote(ctx context.Context, p RemoteConfig) (*Remote, error) {
	if !validControlID(p.ID) || !filepath.IsAbs(p.Socket) || filepath.Clean(p.Socket) != p.Socket || !validProtocol(p.Protocol) || p.Timeout <= 0 {
		return nil, fmt.Errorf("gateway ID, protocol, absolute Unix socket and positive timeout required")
	}
	transport := &http.Transport{
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		ResponseHeaderTimeout: p.Timeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, err := (&net.Dialer{}).DialContext(ctx, "unix", p.Socket)
			if err != nil {
				return nil, err
			}
			uid, err := peerUID(connection)
			if err != nil || uid != p.PeerUID {
				connection.Close()
				return nil, fmt.Errorf("gateway Unix peer does not match configured UID")
			}
			return connection, nil
		},
	}
	r := &Remote{config: p, transport: transport}
	r.client = &http.Client{Transport: transport, Timeout: p.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://gateway/v1/info", nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("gateway %s handshake: %w", p.ID, err)
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if readErr != nil || len(raw) > 4096 || response.StatusCode != http.StatusOK || decodeControlRequest(decoder, &r.info) != nil || r.info.Version != ControlVersion || r.info.ID != p.ID || r.info.Protocol != p.Protocol || !validControlID(r.info.Instance) || response.Header.Get(gatewayInstanceHeader) != r.info.Instance {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("gateway %s control identity or version mismatch", p.ID)
	}
	return r, nil
}

func (r *Remote) Info() ControlInfo { return r.info }

// Close only releases the application's client connections. It does not drain
// the external gateway or terminate upstream work; Drain is explicit.
func (r *Remote) Close() { r.transport.CloseIdleConnections() }

func (r *Remote) Activate(ctx context.Context, binding string, revision uint64) error {
	if !validControlID(binding) || revision == 0 {
		return fmt.Errorf("binding identity and revision required")
	}
	return r.call(ctx, "/v1/bindings/activate", controlRequest{BindingID: binding, Revision: revision})
}
func (r *Remote) Revoke(ctx context.Context, binding string) error {
	if !validControlID(binding) {
		return fmt.Errorf("binding identity required")
	}
	return r.call(ctx, "/v1/bindings/revoke", controlRequest{BindingID: binding})
}
func (r *Remote) RevokeGrant(ctx context.Context, grant string) error {
	if !validControlID(grant) {
		return fmt.Errorf("grant identity required")
	}
	return r.call(ctx, "/v1/grants/revoke", controlRequest{GrantID: grant})
}
func (r *Remote) Drain(ctx context.Context) error {
	return r.call(ctx, "/v1/drain", controlRequest{})
}

func (r *Remote) call(ctx context.Context, path string, p controlRequest) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gateway"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(gatewayInstanceHeader, r.info.Instance)
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("gateway %s operation unresolved: %w", r.config.ID, err)
	}
	defer response.Body.Close()
	// Never treat an unsolicited success from a different process instance as
	// evidence that the pinned gateway stopped its previously established work.
	if response.StatusCode == http.StatusPreconditionFailed || response.Header.Get(gatewayInstanceHeader) != r.info.Instance {
		return fmt.Errorf("gateway %s: %w", r.config.ID, ErrGatewayRestarted)
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("gateway %s operation unresolved: HTTP %d", r.config.ID, response.StatusCode)
	}
	return nil
}

// Remotes drains every configured protocol gateway. One failed drain does not
// suppress attempts at the other gateways, but the composite result succeeds
// only when all have acknowledged actual upstream termination.
type Remotes struct{ gateways []*Remote }

func NewRemotes(ctx context.Context, configs []RemoteConfig) (*Remotes, error) {
	if len(configs) == 0 || len(configs) > 16 {
		return nil, fmt.Errorf("one to sixteen gateway controls required")
	}
	ids, sockets := map[string]bool{}, map[string]bool{}
	for _, p := range configs {
		if ids[p.ID] || sockets[p.Socket] {
			return nil, fmt.Errorf("duplicate gateway identity or control socket")
		}
		ids[p.ID], sockets[p.Socket] = true, true
	}
	r := &Remotes{}
	for _, p := range configs {
		gateway, err := NewRemote(ctx, p)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.gateways = append(r.gateways, gateway)
	}
	return r, nil
}
func (r *Remotes) Close() {
	for _, gateway := range r.gateways {
		gateway.Close()
	}
}

// Activate publishes a binding to every configured gateway. A gateway that does
// not serve this binding's protocol still records the revision, so activation is
// broadcast rather than routed: the application holds one authority record and a
// gateway that never sees traffic for it is simply idle.
//
// It succeeds only when every gateway acknowledged, because a partially
// activated route would answer at some listeners and fail at others.
func (r *Remotes) Activate(ctx context.Context, binding string, revision uint64) error {
	return r.each(func(gateway *Remote) error { return gateway.Activate(ctx, binding, revision) })
}
func (r *Remotes) Revoke(ctx context.Context, binding string) error {
	return r.each(func(gateway *Remote) error { return gateway.Revoke(ctx, binding) })
}
func (r *Remotes) RevokeGrant(ctx context.Context, grant string) error {
	return r.each(func(gateway *Remote) error { return gateway.RevokeGrant(ctx, grant) })
}
func (r *Remotes) each(operation func(*Remote) error) error {
	if len(r.gateways) == 0 {
		return fmt.Errorf("gateway controls are not configured")
	}
	results := make(chan error, len(r.gateways))
	for _, gateway := range r.gateways {
		go func(gateway *Remote) { results <- operation(gateway) }(gateway)
	}
	var failures []error
	for range r.gateways {
		failures = append(failures, <-results)
	}
	return errors.Join(failures...)
}
