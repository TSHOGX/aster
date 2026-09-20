# Network gateways and optional proxies

The application controls reachability and route lifetime. Endpoints name a host,
TCP port and next-hop ID; their purpose is opaque. Database authentication, object
permissions, model allowlists and upstream credentials belong to external services
or separately configured proxies. No application network field selects a protocol
adapter or carries resource permissions.

## Connectivity compilation

The deployment's `connectivity` block declares `revision`, `synthetic_range`,
`protected`, `catalog` and per-driver `policies`. A catalog endpoint contains
`id`, `hostname`, `port`, `transport: "tcp"` and `next_hop`. The next hop selects a
`gateways[].id`, whose `listener` is an IPv4 address and port. Policy `endpoints`
contains objects with `endpoint_id`; `direct` contains `cidr`, `port` and `proto`
(`tcp` or `udp`); `resolvers` explicitly names approved IPv4 DNS servers.

Compilation emits a complete default-deny declaration and matching hostname
projection, including the generation's `/etc/resolv.conf`. Empty resolver lists
clear previous nameservers. It rejects protected/synthetic overlap, direct gateway access, wildcard
ports, undeclared next hops and unsupported steering transports. Names do not
establish authority: enforcement uses exact addresses and ports. Shell and Pi may
receive any explicitly declared endpoint. An absent connectivity block means no
network access. A direct-only policy needs no gateways. Zero-gateway deployments
may omit authorization socket configuration.

Direct permissions last for a running generation; revocation requires confirmed
Khora placement teardown. Managed routes receive turn-scoped grants, whose expiry
also bounds active flows during application outages. Use the mechanism matching the
required lifetime. Connection closure does not undo an upstream business operation.

## Trusted ingress and authorization

`DestinationContext` obtains the original IPv4 TCP destination from
`SO_ORIGINAL_DST`. Guest headers and source addresses cannot supply identity.
Unsteered connections and destinations outside the reserved range are refused.
Khora requires its configured second-hop UID for steering; upstream connections
must originate from the isolated next-hop process under that UID.

`AuthorizationHandler` uses a protected Unix listener with `PeerContext`, which
checks `SO_PEERCRED` against the configured gateway UID. Socket mode is `0660`
with an explicit group. A process lock protects each listener. Startup refuses a
live owner, a live endpoint, symlinks and non-socket paths; after a crash it can
reclaim its own stale socket under that lock. Gateways must drain before releasing
their listener lock and must own their upstream sockets in the tracked process.

`POST /v1/authorize` accepts only generic route identity:

```json
{
  "backend_id": "durable-engine-store",
  "destination": "10.80.0.1:443",
  "endpoint_id": "inference",
  "request_id": "gateway-generated-request-id"
}
```

`endpoint_id` is an optional cross-check, never a way to choose another destination.
Authorization requires an accepted, current execution and a leased/running turn
with a live matching grant. The response supplies session/user/generation/turn,
binding revision, grant ID, policy digest and expiry. `authorized_until` preserves
the original request deadline across lost-reply retries; a finished request ID
cannot open another flow. There are no model, database,
bucket or operation fields. Every new flow or proxy request obtains current
admission, including requests over reused connections.

Denial is 403, conflicting request identity is 409 and unavailable authorization is
503. Unknown fields and trailing JSON are rejected; the application owns the clock.
`POST /v1/finish` accepts `backend_id`, `request_id`, `outcome` and nonnegative
`bytes`. It preserves original attribution across turn changes. Identical retries
succeed; contradictory finishes return 409. If a finish report is lost, confirmed
control-plane teardown records `transport_closed` with unknown bytes. A later
gateway report may enrich that provisional record without changing attribution.

## Gateway control and flow lifetime

`ControlHandler` requires application UID, gateway ID and a bounded timeout.
`GET /v1/info` returns version `1`, `id` and random process `instance`. Both peers
check kernel UIDs. Every response carries `X-Harness-Gateway-Instance`; mutations
require the pinned instance or fail with 412.

| POST endpoint | Body | Successful result |
|---|---|---|
| `/v1/bindings/activate` | `{"binding_id":"binding","revision":1}` | Binding revision registered |
| `/v1/bindings/revoke` | `{"binding_id":"binding"}` | Binding tombstoned and flows stopped |
| `/v1/grants/revoke` | `{"grant_id":"grant"}` | Grant tombstoned and flows stopped |
| `/v1/drain` | `{}` | Admission closed and flows stopped |

Success is exactly 204. Register a flow with `Flows.Begin` before upstream I/O and
use its expiring context. Revocation and admission share a lock. Revocation cancels
flows and waits for `Finish`, which the adapter calls only after transport activity
stops. A timeout is unresolved teardown; cancellation alone is insufficient.
Revoked identities cannot reactivate. Deadline enforcement remains local to the
gateway when the application is unavailable.

`NewRemote` pins the socket's UID, control version, ID, instance and kernel peer
pidfd (`SO_PEERPIDFD`, Linux 6.5 or newer). It disables redirects and proxy
environment variables and bounds requests. A new handshake alone cannot prove
previous-process teardown. Instance replacement requires the previous pidfd to
report exit; a replaced socket while that process lives remains fenced.

`NewRemotes` accepts zero to sixteen unique controls and connects on demand.
Bindings and grants resolve to their original next hop in the product database;
control is never broadcast to unrelated gateways. Each gateway has an independent
bounded reconciliation loop that restores current bindings after verified process
replacement and completes pending grant drains. An unavailable optional gateway
does not prevent daemon startup or control on another next hop.

Every grant revocation creates a durable drain obligation. Completion closes
admission, drains the turn's flows and commits only after the drain receipts exist.
Interrupt, adoption, pause and retirement use the same acknowledgments. Successful
old drains are excluded from adoption work, so large histories do not force every
past grant through the control socket again. Client `Close` releases connections
and process handles; explicit `Drain` stops the gateway's work.

## Separate model proxy

`cmd/model-gateway` runs with its own configuration and UID. Its upstream entries
map `endpoint_id` to `base_url`, `models`, fixed headers and an optional credential
file. Credentials never enter product configuration, state or the guest. Model
restrictions are enforced inside the proxy after generic application admission.
Only native `POST /v1/messages` (including `beta=true`) is forwarded.
Other paths are refused before credential use. Guest authorization and cookie headers are discarded. Upstream statuses are
forwarded and recorded as outcomes. Pi endpoint/model selection belongs to authored
native configuration, independently of network policy.

Pi selects its provider/model from authored `settings.json` (`defaultProvider` and
`defaultModel`) and resolves the origin from native `models.json`. The bridge's
`GET /healthz` returns a bounded local response only after `POST /v1/route` resolves
a current destination and the gateway confirms its activated binding revision.
This lookup permits the generation's exact pending start operation and issues no
grant. All other pending lifecycle changes fence it out. Health never contacts
an upstream; workload forwarding still requires an accepted turn grant.

## Durable destination allocation

`ReserveEgressCompilation` requires current owner, session/environment identity,
fence and target epoch. Initial environments plan epoch 1; retained environments
must be paused and plan their next epoch. Unresolved lifecycle/recovery operations,
retirement and blocked engine identities prevent fresh allocation.

Each engine store pins one canonical private IPv4 `/8` through `/30` pool. A
managed endpoint consumes one address, excluding network/broadcast, for the life
of that store identity. Revoked allocations remain tombstones. Pool advancement,
immutable allocation and route rows commit in one transaction. Exhaustion returns
`egress.ErrTupleExhausted` without recycling or expanding the pool.

Retries preserve original bindings and intent. A changed fence, epoch, policy,
next hop or range fails. The compiler's complete network declaration replaces the
plan's network before launch. Configuration observation precedes activation;
bridge readiness and execution acceptance precede turn grants.

The model process also bounds traffic independently of control: `max_connections`
(default 64) includes idle TCP connections, and `max_requests` (default 16) bounds
concurrent body parsing and forwarding. Request bodies are limited to 8 MiB.
Grant cancellation sets the guest response write deadline as well as canceling
upstream I/O, so a guest that stops reading cannot prevent flow teardown.
