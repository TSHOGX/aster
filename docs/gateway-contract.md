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
projection. It rejects protected/synthetic overlap, direct gateway access, wildcard
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
with an explicit group. An existing socket is never replaced implicitly.

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
binding revision, grant ID, policy digest and expiry. There are no model, database,
bucket or operation fields. Every new flow or proxy request obtains current
admission, including requests over reused connections.

Denial is 403, conflicting request identity is 409 and unavailable authorization is
503. Unknown fields and trailing JSON are rejected; the application owns the clock.
`POST /v1/finish` accepts `backend_id`, `request_id`, `outcome` and nonnegative
`bytes`. It preserves original attribution across turn changes. Identical retries
succeed; contradictory finishes return 409.

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

`NewRemote` pins the socket's UID, control version, ID and instance, disables
redirects/proxy environment variables and bounds control requests. Lost replies
retry against the same instance. `ErrGatewayRestarted` requires reconciliation;
a new handshake alone cannot prove the prior process's flows stopped.
`NewRemotes` accepts zero to sixteen unique controls. Client `Close` releases idle
control connections; explicit `Drain` stops gateway work.

## Separate model proxy

`cmd/model-gateway` runs with its own configuration and UID. Its upstream entries
map `endpoint_id` to `base_url`, `models`, fixed headers and an optional credential
file. Credentials never enter product configuration, state or the guest. Model
restrictions are enforced inside the proxy after generic application admission.
Guest authorization and cookie headers are discarded. Upstream statuses are
forwarded and recorded as outcomes. Pi endpoint/model selection belongs to authored
native configuration, independently of network policy.

Ordinary Pi startup additionally requires local pre-turn readiness on an activated
binding. The full readiness and real-engine gateway journey remains under active
integration; unit/control tests alone do not establish that evidence.

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
