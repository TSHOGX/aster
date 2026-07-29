package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuthorizeDataProxyRequestGrantsDuringActiveTurn(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	allocation, turnID, sandboxSourceIP := createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_ok", now)

	auth, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
		Domain:          DataProxyDomainDoris,
		SandboxSourceIP: sandboxSourceIP,
		Now:             now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("authorize during an active turn: %v", err)
	}
	if auth.SessionID != "sess_dp_ok" || auth.GenerationID != allocation.GenerationID || auth.TurnID != turnID {
		t.Errorf("auth = %+v, want session sess_dp_ok / gen %s / turn %d",
			auth, allocation.GenerationID, turnID)
	}
	if auth.Domain != DataProxyDomainDoris {
		t.Errorf("Domain = %q, want %q", auth.Domain, DataProxyDomainDoris)
	}
}

// The load-bearing invariant: authorization must be a pure decision. Bumping
// next_request_sequence would corrupt model-proxy request sequencing, because
// events_proxy_started_request_uq is a unique partial index keyed on the
// proxy_request_id derived from it — i.e. it would break the live model path.
func TestAuthorizeDataProxyRequestDoesNotTouchRequestSequence(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	_, _, sandboxSourceIP := createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_seq", now)

	readSequence := func() int64 {
		t.Helper()
		var seq int64
		if err := st.db.QueryRowContext(ctx,
			`SELECT next_request_sequence FROM active_model_request_contexts WHERE sandbox_source_ip = ?`,
			sandboxSourceIP).Scan(&seq); err != nil {
			t.Fatalf("read next_request_sequence: %v", err)
		}
		return seq
	}

	before := readSequence()
	for i := 0; i < 5; i++ {
		if _, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
			Domain:          DataProxyDomainDoris,
			SandboxSourceIP: sandboxSourceIP,
			Now:             now.Add(time.Duration(i+1) * time.Second),
		}); err != nil {
			t.Fatalf("authorize %d: %v", i, err)
		}
	}
	if after := readSequence(); after != before {
		t.Errorf("next_request_sequence moved from %d to %d; the dataplane must not touch the model proxy's counter",
			before, after)
	}

	// And the model proxy's own sequencing still starts where it should.
	started, err := st.StartProxyRequest(ctx, StartProxyRequestParams{
		SandboxSourceIP: sandboxSourceIP,
		ProxyRequestID:  "proxy_after_dataplane",
		UpstreamModel:   "claude-sonnet",
		UpstreamBaseURL: "https://api.anthropic.test",
		Now:             now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("model proxy start after dataplane authorizations: %v", err)
	}
	if started.RequestSequence != 1 {
		t.Errorf("model proxy RequestSequence = %d, want 1; the dataplane consumed a sequence number",
			started.RequestSequence)
	}
}

// Authorization must emit no events. The events table is at its retention cap, and
// a per-query event pair would be pure churn.
func TestAuthorizeDataProxyRequestEmitsNoEvents(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	_, _, sandboxSourceIP := createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_ev", now)

	countEvents := func() int64 {
		t.Helper()
		var n int64
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
			t.Fatalf("count events: %v", err)
		}
		return n
	}

	before := countEvents()
	for i := 0; i < 3; i++ {
		if _, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
			Domain:          DataProxyDomainDoris,
			SandboxSourceIP: sandboxSourceIP,
			Now:             now.Add(time.Duration(i+1) * time.Second),
		}); err != nil {
			t.Fatalf("authorize %d: %v", i, err)
		}
	}
	if after := countEvents(); after != before {
		t.Errorf("events grew from %d to %d; authorization must be a pure decision", before, after)
	}
}

func TestAuthorizeDataProxyRequestDeniesUnknownSourceIP(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_unknown", now)

	// An IP with no registered context: this is the case where a sandbox tries
	// to reach the gateway from somewhere it should not be able to.
	_, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
		Domain:          DataProxyDomainDoris,
		SandboxSourceIP: "10.255.255.254",
		Now:             now.Add(time.Second),
	})
	if !errors.Is(err, ErrDataProxyContextUnavailable) {
		t.Errorf("err = %v, want ErrDataProxyContextUnavailable", err)
	}
	if reason := st.DataProxyDenialReason(ctx, "10.255.255.254"); reason != "unknown sandbox" {
		t.Errorf("reason = %q, want %q", reason, "unknown sandbox")
	}
}

// A missing peer address must be an error, never a grant: the source IP is the
// only part of the request a sandbox cannot forge, so authorizing without one
// would remove the boundary entirely.
func TestAuthorizeDataProxyRequestRequiresSourceIP(t *testing.T) {
	ctx := context.Background()
	st, _ := openOwnedStore(t, ctx)

	for _, ip := range []string{"", "   "} {
		auth, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
			Domain:          DataProxyDomainDoris,
			SandboxSourceIP: ip,
			Now:             time.Now().UTC(),
		})
		if err == nil {
			t.Fatalf("an empty source ip (%q) was authorized: %+v", ip, auth)
		}
		if !strings.Contains(err.Error(), "source ip") {
			t.Errorf("err = %v, want it to name the missing source ip", err)
		}
	}
}

// The turn boundary is the point: access is valid only during the sandbox's own
// active turn, which is what makes the fake credential harmless on its own.
func TestAuthorizeDataProxyRequestDeniedAfterTurnFinishes(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	allocation, turnID, sandboxSourceIP := createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_finish", now)

	if _, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
		Domain:          DataProxyDomainDoris,
		SandboxSourceIP: sandboxSourceIP,
		Now:             now.Add(time.Second),
	}); err != nil {
		t.Fatalf("expected an initial grant: %v", err)
	}

	// End the turn the same way the control plane does.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE turns SET status = 'completed' WHERE id = ? AND session_id = ? AND generation_id = ?`,
		turnID, "sess_dp_finish", allocation.GenerationID); err != nil {
		t.Fatalf("finish turn: %v", err)
	}

	_, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
		Domain:          DataProxyDomainDoris,
		SandboxSourceIP: sandboxSourceIP,
		Now:             now.Add(2 * time.Second),
	})
	if !errors.Is(err, ErrDataProxyContextUnavailable) {
		t.Errorf("err = %v, want a denial once the turn finished", err)
	}
	// The reason must be intelligible to the agent that reads it in an ERR
	// packet: "no active turn" tells it to reopen inside a turn.
	if reason := st.DataProxyDenialReason(ctx, sandboxSourceIP); reason != "no active turn" {
		t.Errorf("reason = %q, want %q", reason, "no active turn")
	}
}

// The domain is carried for logging and future per-domain policy, but the decision
// rests on the source IP. A gateway naming a different domain must not thereby
// gain or lose access.
func TestAuthorizeDataProxyRequestDecisionIsIndependentOfDomain(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	_, _, sandboxSourceIP := createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_domain", now)

	for _, domain := range []DataProxyDomain{DataProxyDomainDoris, DataProxyDomainOBS, "something_else"} {
		auth, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
			Domain:          domain,
			SandboxSourceIP: sandboxSourceIP,
			Now:             now.Add(time.Second),
		})
		if err != nil {
			t.Errorf("domain %q was denied: %v", domain, err)
			continue
		}
		if auth.Domain != domain {
			t.Errorf("Domain = %q, want it echoed back as %q", auth.Domain, domain)
		}
	}
}

func TestAuthorizeDataProxyRequestDefaultsNow(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	now := time.Now().UTC()
	_, _, sandboxSourceIP := createRunningProxyTurn(t, ctx, st, owner.UUID, "sess_dp_now", now)

	// A zero Now must not silently authorize against the epoch.
	if _, err := st.AuthorizeDataProxyRequest(ctx, AuthorizeDataProxyRequestParams{
		Domain:          DataProxyDomainDoris,
		SandboxSourceIP: sandboxSourceIP,
	}); err != nil {
		t.Errorf("zero Now was not defaulted to the current time: %v", err)
	}
}
