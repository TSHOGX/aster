package servicegateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/product"
)

const AuthorityGrace = 5 * time.Second
const authorityTimeout = time.Second

var ErrAuthorityUnavailable = errors.New("service authority unavailable")

// RemoteAuthority talks only to the protected owner socket. It never opens the
// database or holds upstream credentials. Each occupied slot remains occupied
// until its release is durably acknowledged, including failed/lost acquires.
type RemoteAuthority struct {
	client             *http.Client
	transport          *http.Transport
	audience, instance string
	free               chan *product.ServiceRequest
	cleanup            chan *product.ServiceRequest
	ctx                context.Context
	cancel             context.CancelFunc
	workers            sync.WaitGroup
	pending            atomic.Int64
}

// NewRemoteAuthority requires the gateway's exclusive lifetime lock. Keep that
// lock through all forwarding and Close; a replacement registration clears the
// previous instance's unresolved slots because that process can no longer send.
func NewRemoteAuthority(ctx context.Context, socket, audience string, slots int) (*RemoteAuthority, error) {
	if !filepath.IsAbs(socket) || audience == "" || slots < 1 || slots > 256 {
		return nil, product.ErrInvalid
	}
	ctx, cancel := context.WithCancel(ctx)
	a := &RemoteAuthority{audience: audience, instance: uuid.NewString(), free: make(chan *product.ServiceRequest, slots), cleanup: make(chan *product.ServiceRequest, slots), ctx: ctx, cancel: cancel}
	a.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 16, MaxIdleConnsPerHost: 16, MaxConnsPerHost: 272, IdleConnTimeout: 30 * time.Second}
	a.client = &http.Client{Transport: a.transport, Timeout: authorityTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	command := authorityCommand{Request: product.ServiceRequest{Audience: audience, Instance: a.instance}}
	var registration product.GatewayRegistration
	if err := a.call(ctx, "registration", command, &registration); err != nil {
		a.Close()
		return nil, err
	}
	command.Expected = registration.Generation
	if err := a.call(ctx, "register", command, &registration); err != nil {
		a.Close()
		return nil, err
	}
	if registration.Instance != a.instance || registration.Slots != slots {
		a.Close()
		return nil, errors.New("gateway capacity differs from authority configuration")
	}
	for slot := 0; slot < slots; slot++ {
		a.free <- &product.ServiceRequest{Audience: audience, Instance: a.instance, Slot: slot}
	}
	for n := 0; n < min(slots, 8); n++ {
		a.workers.Add(1)
		go a.clean()
	}
	return a, nil
}

func (a *RemoteAuthority) call(ctx context.Context, action string, command authorityCommand, out any) error {
	raw, err := json.Marshal(command)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://owner"+AuthorityPath+action, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return ErrAuthorityUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		decoder := json.NewDecoder(io.LimitReader(response.Body, 4097))
		if err = decoder.Decode(out); err != nil {
			return ErrAuthorityUnavailable
		}
		if decoder.Decode(new(any)) != io.EOF {
			return ErrAuthorityUnavailable
		}
		return nil
	case http.StatusForbidden:
		return product.ErrNotFound
	case http.StatusBadRequest:
		return product.ErrInvalid
	case http.StatusLocked:
		return product.ErrDraining
	case http.StatusTooManyRequests:
		return product.ErrCapacity
	default:
		return ErrAuthorityUnavailable
	}
}

func (a *RemoteAuthority) AcquireService(ctx context.Context, audience, token string) (product.Attribution, func(), error) {
	if audience != a.audience {
		return product.Attribution{}, nil, product.ErrNotFound
	}
	if a.ctx.Err() != nil {
		return product.Attribution{}, nil, ErrAuthorityUnavailable
	}
	var slot *product.ServiceRequest
	select {
	case slot = <-a.free:
	default:
		return product.Attribution{}, nil, product.ErrCapacity
	}
	a.pending.Add(1)
	slot.Sequence++
	var once sync.Once
	release := func() { once.Do(func() { a.cleanup <- slot }) }
	var out product.Attribution
	err := a.call(ctx, "acquire", authorityCommand{Request: *slot, Token: token}, &out)
	if err == nil && (out.Request == nil || *out.Request != *slot || out.Grant.Audience != audience || out.TurnID == "" || out.Grant.ID == "") {
		err = ErrAuthorityUnavailable
	}
	if err != nil {
		release()
		return product.Attribution{}, nil, err
	}
	return out, release, nil
}

func (a *RemoteAuthority) HoldsAttribution(ctx context.Context, held product.Attribution) error {
	if held.Request == nil || held.Request.Instance != a.instance || held.Request.Audience != a.audience {
		return product.ErrNotFound
	}
	return a.call(ctx, "check", authorityCommand{Request: *held.Request}, &struct{}{})
}

func (a *RemoteAuthority) clean() {
	defer a.workers.Done()
	for {
		var slot *product.ServiceRequest
		select {
		case <-a.ctx.Done():
			return
		case slot = <-a.cleanup:
		}
		for {
			err := a.call(a.ctx, "release", authorityCommand{Request: *slot}, &struct{}{})
			// A replacement instance has already retired this slot. Never attempt
			// to re-register automatically or transfer its request to that owner.
			if err == nil || errors.Is(err, product.ErrNotFound) {
				a.pending.Add(-1)
				a.free <- slot
				break
			}
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-a.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (a *RemoteAuthority) Pending() int64 { return a.pending.Load() }

// Close is called after upstream forwarding has stopped. Unacknowledged rows
// remain release blockers until the next exclusively locked gateway starts.
func (a *RemoteAuthority) Close() {
	a.cancel()
	a.workers.Wait()
	if a.transport != nil {
		a.transport.CloseIdleConnections()
	}
}
