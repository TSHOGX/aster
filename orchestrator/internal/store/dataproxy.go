package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Data-plane authorization for host-side gateways (Doris, OBS).
//
// This is deliberately split from the model-proxy path (proxy.go), per the shape
// agreed with the OBS plan: AuthorizeDataProxyRequest is a PURE DECISION — no
// events, no counters, no writes of any kind. Domains that want per-request
// events add their own recorder on top.
//
// Two constraints make that split mandatory rather than stylistic:
//
//  1. **next_request_sequence must not be touched.** StartProxyRequest CAS-bumps
//     that counter, and events_proxy_started_request_uq /
//     events_proxy_finished_request_uq are unique partial indexes on
//     proxy_request_id for the model-proxy event types. Sharing the counter would
//     corrupt model-proxy sequencing — i.e. break the live model request path.
//     Nothing here writes to it.
//  2. **The events table is at its retention cap.** A per-query event pair at
//     the observed query volume would be churn for little value, so the Doris
//     gateway logs per-query detail to its own JSONL and emits only
//     per-connection events.
//
// Correlation is by source IP and never by anything the sandbox says. Each
// generation owns a /30 with a fixed sandbox IP, so the TCP peer address is an
// identity the sandbox cannot forge from inside.

// ErrDataProxyContextUnavailable reports that a source IP has no live, active
// turn to authorize against. It is deliberately distinct from a database error:
// callers must treat this as a denial and an error as a denial too, never as an
// allow.
var ErrDataProxyContextUnavailable = errors.New("data proxy context unavailable")

// DataProxyDomain identifies which gateway is asking.
type DataProxyDomain string

const (
	// DataProxyDomainDoris is the Doris MySQL gateway.
	DataProxyDomainDoris DataProxyDomain = "doris"
	// DataProxyDomainOBS is the OBS gateway (sibling plan).
	DataProxyDomainOBS DataProxyDomain = "obs"
)

// AuthorizeDataProxyRequestParams describes one authorization question.
type AuthorizeDataProxyRequestParams struct {
	// Domain is the asking gateway. It is carried for logging and future
	// per-domain policy; the decision itself rests on SandboxSourceIP, because
	// that is the only part a sandbox cannot forge.
	Domain DataProxyDomain
	// SandboxSourceIP is the TCP peer address of the sandbox connection.
	SandboxSourceIP string
	Now             time.Time
}

// DataProxyAuthorization is the turn context a granted request belongs to.
type DataProxyAuthorization struct {
	SessionID    string
	GenerationID string
	TurnID       int64
	Domain       DataProxyDomain
}

// AuthorizeDataProxyRequest reports whether the sandbox at SandboxSourceIP may
// use a data proxy right now.
//
// Every condition from the model-proxy join is preserved — source IP tied to a
// live runtime instance, a running turn whose lease is unexpired and owned by the
// registered lease owner, an active-or-idle generation that is the session's
// active one, a non-failed session, and exactly one in-flight turn — with two
// deliberate differences:
//
//   - the model_access_allowed conditions are dropped, so a data-only profile
//     (no model access) can still read through a gateway;
//   - next_request_sequence is neither read nor written.
func (s *Store) AuthorizeDataProxyRequest(ctx context.Context, p AuthorizeDataProxyRequestParams) (DataProxyAuthorization, error) {
	sourceIP := strings.TrimSpace(p.SandboxSourceIP)
	if sourceIP == "" {
		// Without a peer address there is no identity to authorize, and the
		// sandbox must never be able to supply one itself.
		return DataProxyAuthorization{}, fmt.Errorf("data proxy authorization requires a sandbox source ip")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}

	var auth DataProxyAuthorization
	auth.Domain = p.Domain

	now := formatTime(p.Now)
	err := s.db.QueryRowContext(ctx, `
SELECT c.session_id, c.generation_id, c.turn_id
FROM active_model_request_contexts c
JOIN turns t ON t.id = c.turn_id
  AND t.session_id = c.session_id
  AND t.generation_id = c.generation_id
JOIN runtime_generations g ON g.generation_id = c.generation_id
  AND g.session_id = c.session_id
JOIN runtime_resource_instances ri ON ri.generation_id = c.generation_id
  AND ri.session_id = c.session_id
JOIN sessions s ON s.id = c.session_id
WHERE c.sandbox_source_ip = ?
  AND ri.state = 'live'
  AND ri.sandbox_ip = c.sandbox_source_ip
  AND c.expires_at > ?
  AND t.status = 'running'
  AND t.lease_owner = c.lease_owner
  AND t.lease_expires_at > ?
  AND g.status IN ('active','idle')
  AND g.lease_owner = c.lease_owner
  AND g.lease_expires_at > ?
  AND s.active_generation_id = c.generation_id
  AND s.status NOT IN ('failed', 'destroyed')
  AND (
    SELECT COUNT(*)
    FROM turns inflight
    WHERE inflight.session_id = c.session_id
      AND inflight.generation_id = c.generation_id
      AND inflight.status IN ('leased', 'running')
      AND inflight.lease_owner = c.lease_owner
      AND inflight.lease_expires_at > ?
  ) = 1`,
		sourceIP, now, now, now, now,
	).Scan(&auth.SessionID, &auth.GenerationID, &auth.TurnID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DataProxyAuthorization{}, ErrDataProxyContextUnavailable
		}
		return DataProxyAuthorization{}, err
	}
	return auth, nil
}

// DataProxyDenialReason returns a short, agent-readable explanation for a denial.
//
// The gateway puts this in an ERR packet the agent reads, so "no active turn" is
// worth distinguishing from "unknown sandbox": the first tells an agent to reopen
// its connection inside a turn, the second means something is misconfigured. This
// is diagnostic only and is never consulted to grant access.
func (s *Store) DataProxyDenialReason(ctx context.Context, sourceIP string) string {
	sourceIP = strings.TrimSpace(sourceIP)
	if sourceIP == "" {
		return "no sandbox source ip"
	}

	var registered int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM active_model_request_contexts WHERE sandbox_source_ip = ?`,
		sourceIP).Scan(&registered); err != nil || registered == 0 {
		return "unknown sandbox"
	}

	var liveInstance int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM active_model_request_contexts c
JOIN runtime_resource_instances ri ON ri.generation_id = c.generation_id
  AND ri.session_id = c.session_id
  AND ri.sandbox_ip = c.sandbox_source_ip
WHERE c.sandbox_source_ip = ?
  AND ri.state = 'live'`, sourceIP).Scan(&liveInstance); err == nil && liveInstance == 0 {
		return "sandbox is no longer running"
	}

	return "no active turn"
}
