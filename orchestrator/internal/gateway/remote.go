package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/egress"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var ErrGatewayRestarted = errors.New("gateway instance changed; reconciliation required")

type RemoteConfig struct {
	ID, Socket string
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
	peerMu    sync.Mutex
	peerPID   int32
	pidFD     int
	closed    bool
}

func NewRemote(ctx context.Context, p RemoteConfig) (*Remote, error) {
	if !validControlID(p.ID) || !filepath.IsAbs(p.Socket) || filepath.Clean(p.Socket) != p.Socket || p.Timeout <= 0 {
		return nil, fmt.Errorf("gateway ID, absolute Unix socket and positive timeout required")
	}
	r := &Remote{config: p, pidFD: -1}
	transport := &http.Transport{
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		ResponseHeaderTimeout: p.Timeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, err := DialControl(ctx, p.Socket, p.PeerUID)
			if err != nil {
				return nil, err
			}
			credential, err := peerCredentials(connection)
			if err == nil {
				err = r.pinPeer(connection, credential.Pid)
			}
			if err != nil {
				_ = connection.Close()
				return nil, err
			}

			return connection, nil
		},
	}
	r.transport = transport
	r.client = &http.Client{Transport: transport, Timeout: p.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://gateway/v1/info", nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("gateway %s handshake: %w", p.ID, err)
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if readErr != nil || len(raw) > 4096 || response.StatusCode != http.StatusOK || decodeControlRequest(decoder, &r.info) != nil || r.info.Version != ControlVersion || r.info.ID != p.ID || !validControlID(r.info.Instance) || response.Header.Get(gatewayInstanceHeader) != r.info.Instance {
		r.Close()
		return nil, fmt.Errorf("gateway %s control identity or version mismatch", p.ID)
	}
	return r, nil
}

func (r *Remote) Info() ControlInfo { return r.info }

// Close only releases the application's client connections. It does not drain
// the external gateway or terminate upstream work; Drain is explicit.
func (r *Remote) Close() {
	r.peerMu.Lock()
	defer r.peerMu.Unlock()
	r.closed = true
	r.transport.CloseIdleConnections()
	if r.pidFD >= 0 {
		_ = unix.Close(r.pidFD)
		r.pidFD = -1
	}
}

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

func (r *Remote) pinPeer(connection net.Conn, pid int32) error {
	r.peerMu.Lock()
	defer r.peerMu.Unlock()
	if r.closed {
		return fmt.Errorf("gateway client closed")
	}
	if r.peerPID != 0 {
		if pid != r.peerPID || pidfdExited(r.pidFD) {
			return ErrGatewayRestarted
		}
		return nil
	}
	socket := connection.(*net.UnixConn)
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	fd := -1
	var pinErr error
	if err := raw.Control(func(socketFD uintptr) {
		fd, pinErr = unix.GetsockoptInt(int(socketFD), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	}); err != nil {
		return err
	}
	if pinErr != nil {
		return fmt.Errorf("pin gateway process lifetime (SO_PEERPIDFD required): %w", pinErr)
	}
	unix.CloseOnExec(fd)
	r.peerPID, r.pidFD = pid, fd
	return nil
}
func (r *Remote) processExited() bool {
	r.peerMu.Lock()
	defer r.peerMu.Unlock()
	return pidfdExited(r.pidFD)
}

type RouteDirectory interface {
	PendingGatewayGrantDrains(context.Context, string, string, int) ([]string, error)
	ConfirmGatewayGrantDrained(context.Context, string) error
	ConfirmGatewayBindingDrained(context.Context, string) error
	BindingNextHop(context.Context, string) (string, error)
	GrantNextHop(context.Context, string) (string, error)
	ActiveGatewayBindings(context.Context, string, string, int) ([]egress.BindingAuthority, error)
}

type managedRemote struct {
	mu        sync.Mutex
	config    RemoteConfig
	remote    *Remote
	observed  time.Time
	lastError string
}

// Remotes connects only when a destination needs its next hop. A down optional
// gateway therefore cannot prevent startup or unrelated local work.
type Remotes struct {
	closed    atomic.Bool
	gateways  map[string]*managedRemote
	directory RouteDirectory
}

func NewRemotes(_ context.Context, configs []RemoteConfig, directory RouteDirectory) (*Remotes, error) {
	if len(configs) > 16 || (len(configs) > 0 && directory == nil) {
		return nil, fmt.Errorf("at most sixteen gateways and a route directory required")
	}
	r := &Remotes{gateways: map[string]*managedRemote{}, directory: directory}
	sockets := map[string]bool{}
	for _, p := range configs {
		if !validControlID(p.ID) || !filepath.IsAbs(p.Socket) || filepath.Clean(p.Socket) != p.Socket || p.Timeout <= 0 || r.gateways[p.ID] != nil || sockets[p.Socket] {
			return nil, fmt.Errorf("unique gateway identities, absolute sockets and positive bounds required")
		}
		r.gateways[p.ID] = &managedRemote{config: p}
		sockets[p.Socket] = true
	}
	return r, nil
}
func (r *Remotes) Close() {
	r.closed.Store(true)
	for _, g := range r.gateways {
		g.mu.Lock()
		if g.remote != nil {
			g.remote.Close()
			g.remote = nil
		}
		g.mu.Unlock()
	}
}
func (r *Remotes) Activate(ctx context.Context, binding string, revision uint64) error {
	if r.directory == nil {
		return fmt.Errorf("gateway route directory required")
	}
	id, err := r.directory.BindingNextHop(ctx, binding)
	if err != nil {
		return err
	}
	return r.call(ctx, id, func(remote *Remote) error { return remote.Activate(ctx, binding, revision) })
}
func (r *Remotes) Revoke(ctx context.Context, binding string) error {
	if r.directory == nil {
		return fmt.Errorf("gateway route directory required")
	}
	id, err := r.directory.BindingNextHop(ctx, binding)
	if err != nil {
		return err
	}
	if err := r.call(ctx, id, func(remote *Remote) error { return remote.Revoke(ctx, binding) }); err != nil {
		return err
	}
	return r.directory.ConfirmGatewayBindingDrained(ctx, binding)
}
func (r *Remotes) RevokeGrant(ctx context.Context, grant string) error {
	if r.directory == nil {
		return fmt.Errorf("gateway route directory required")
	}
	id, err := r.directory.GrantNextHop(ctx, grant)
	if err != nil {
		return err
	}
	if err := r.call(ctx, id, func(remote *Remote) error { return remote.RevokeGrant(ctx, grant) }); err != nil {
		return err
	}
	return r.directory.ConfirmGatewayGrantDrained(ctx, grant)
}
func (r *Remotes) call(ctx context.Context, id string, operation func(*Remote) error) (result error) {
	g := r.gateways[id]
	if g == nil {
		return fmt.Errorf("next hop %q is not configured", id)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	defer func() {
		g.observed = time.Now().UTC()
		g.lastError = ""
		if result != nil {
			g.lastError = result.Error()
		}
	}()
	if r.closed.Load() {
		return fmt.Errorf("gateway clients closed")
	}
	// Authenticate the current socket path even when an old keep-alive remains
	// open. Replacing a socket does not prove that the old process stopped.
	fresh, err := NewRemote(ctx, g.config)
	if err != nil {
		return err
	}
	if g.remote == nil {
		g.remote = fresh
	} else if fresh.info.Instance == g.remote.info.Instance {
		fresh.Close()
	} else {
		if !g.remote.processExited() {
			fresh.Close()
			return fmt.Errorf("next hop %s previous process is still alive: %w", id, ErrGatewayRestarted)
		}
		g.remote.Close()
		g.remote = fresh
	}
	return operation(g.remote)
}

// Run restores only currently authoritative bindings after a verified process
// replacement. Each next hop has its own bounded loop and keyset cursor.
func (r *Remotes) Run(ctx context.Context, interval, timeout time.Duration, onError func(string, error)) error {
	if interval <= 0 || timeout <= 0 {
		return fmt.Errorf("positive gateway reconciliation bounds required")
	}
	var workers sync.WaitGroup
	for id := range r.gateways {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			after := ""
			drainAfter := ""
			for ctx.Err() == nil {
				action, cancel := context.WithTimeout(ctx, timeout)
				bindings, err := r.directory.ActiveGatewayBindings(action, id, after, 16)
				cancel()
				if err != nil && onError != nil {
					onError(id, err)
				}
				if len(bindings) == 0 {
					after = ""
				}
				for _, binding := range bindings {
					after = binding.ID
					action, cancel := context.WithTimeout(ctx, timeout)
					err := r.call(action, id, func(remote *Remote) error { return remote.Activate(action, binding.ID, binding.Revision) })
					cancel()
					if err != nil && onError != nil && ctx.Err() == nil {
						onError(id, err)
					}
				}
				action, cancel = context.WithTimeout(ctx, timeout)
				drains, err := r.directory.PendingGatewayGrantDrains(action, id, drainAfter, 16)
				cancel()
				if err != nil && onError != nil {
					onError(id, err)
				}
				if len(drains) == 0 {
					drainAfter = ""
				}
				for _, grant := range drains {
					drainAfter = grant
					action, cancel := context.WithTimeout(ctx, timeout)
					err := r.RevokeGrant(action, grant)
					cancel()
					if err != nil && onError != nil && ctx.Err() == nil {
						onError(id, err)
					}
				}
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(id)
	}
	<-ctx.Done()
	workers.Wait()
	return ctx.Err()
}

// GatewayStatus reports control readiness without exposing routes or secrets.
type GatewayStatus struct {
	ID         string    `json:"id"`
	Instance   string    `json:"instance,omitempty"`
	Ready      bool      `json:"ready"`
	ObservedAt time.Time `json:"observed_at"`
	Error      string    `json:"error,omitempty"`
}

func (r *Remotes) Status() []GatewayStatus {
	result := make([]GatewayStatus, 0, len(r.gateways))
	for id, g := range r.gateways {
		g.mu.Lock()
		status := GatewayStatus{ID: id, ObservedAt: g.observed, Error: g.lastError}
		if g.remote != nil {
			status.Instance = g.remote.Info().Instance
			status.Ready = !r.closed.Load() && status.Error == "" && !g.remote.processExited()
		}
		g.mu.Unlock()
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func pidfdExited(fd int) bool {
	if fd < 0 {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n > 0 && fds[0].Revents&unix.POLLIN != 0
}
