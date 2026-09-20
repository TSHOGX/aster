package egress

import (
	"errors"
	"net/netip"
	"time"
)

var ErrDenied = errors.New("egress request is not authorized")
var ErrRequestConflict = errors.New("egress request identity reused with different intent")

type AuthorizationRequest struct {
	// Destination comes from trusted ingress, never a guest header or source IP.
	BackendID   string         `json:"backend_id"`
	Destination netip.AddrPort `json:"destination"`
	// EndpointID is an optional cross-check. The destination already names the
	// service, because the product reserved one address per service; an adapter
	// that cannot derive the name from a kernel address leaves this empty.
	EndpointID string    `json:"endpoint_id,omitempty"`
	RequestID  string    `json:"request_id"`
	Now        time.Time `json:"-"`
}
type Authorization struct {
	RequestContext
	PolicyDigest string    `json:"policy_digest"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type RequestFinish struct {
	RequestID string    `json:"request_id"`
	BackendID string    `json:"backend_id"`
	Outcome   string    `json:"outcome"`
	Bytes     int64     `json:"bytes"`
	Now       time.Time `json:"-"`
}

// RouteRequest resolves an immutable destination for local readiness only.
// It carries no grant and cannot authorize forwarded traffic.
type RouteRequest struct {
	BackendID   string         `json:"backend_id"`
	Destination netip.AddrPort `json:"destination"`
	Now         time.Time      `json:"-"`
}
type RouteIdentity struct {
	BindingID       string `json:"binding_id"`
	BindingRevision uint64 `json:"binding_revision"`
	EndpointID      string `json:"endpoint_id"`
}

// BindingAuthority names one live immutable binding for gateway reconciliation.
type BindingAuthority struct {
	ID       string
	Revision uint64
}
