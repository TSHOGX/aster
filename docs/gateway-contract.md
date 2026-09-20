# Gateway ingress and authorization

`internal/gateway` provides trusted ingress and flow-lifetime primitives, and the
model HTTP adapter that uses them. Doris MySQL and OBS HTTP adapters are not
implemented here; those deployments must compose these primitives with their own
credential and protocol logic. The Khora daemon serves the authorization contract
and drives every configured gateway's remote control.

## Model HTTP adapter

`NewModelGateway` terminates steered model generation, and `cmd/model-gateway`
runs it as its own process under its own UID. It is an HTTP handler rather than a
raw relay because the resource being authorized — the model — is a body field, so
the adapter must parse the request to name what it is asking for.

The destination comes from the kernel through `DestinationContext`, so a guest
that sets `Host` or `Authorization` can neither select a service nor supply a
credential. Every request is authorized, including one reusing an established
connection, and each carries its own request identity. Guest authorization
headers are dropped; only the gateway's configured credential reaches the
upstream, read from a file that must not be group or world accessible.

The adapter registers each authorized request as a flow before upstream I/O and
calls `Finish` only after upstream activity has stopped, which is what makes a
revocation acknowledgment mean something. Upstream status codes pass through and
are recorded as the request outcome. Unauthorized, denied and unavailable
authorization all fail closed without contacting the upstream.

The engine refuses an `egress.via` declaration unless it was started with a
second-hop UID, because the endpoint process's own egress can only be pinned by
uid. A deployment must therefore run its protocol gateways under that identity.

## Connection attribution

Recover the original destination from the first accepted IPv4 TCP socket before
TLS or relay wrapping. `AcceptDestination` requires a destination within the
configured private service range and rejects direct listener connections.
`OriginalDestination` uses the kernel's `SO_ORIGINAL_DST`; source IP and guest
headers are never an authorization principal.

Khora's declaration supplies exact per-binding `/32 + TCP port` routes to a shared
listener for that protocol. The deployment must isolate listener exposure and
run every upstream socket opener under the configured dedicated gateway UID.
Application inspection requires the pinned engine with manual cold recovery,
verified teardown and conditional mutation support. These ingress helpers alone
do not prove the complete network or established-flow isolation contract.

## Unix authorization control

`AuthorizationHandler` requires `PeerContext` on a protected Unix listener.
`PeerContext` reads `SO_PEERCRED`; only the configured UID is accepted. The socket
uses mode `0660` and the explicitly configured gateway group. The listener refuses
to replace an existing socket. No header can supply peer identity.

`POST /v1/authorize` accepts:

```json
{
  "backend_id": "durable-engine-store",
  "destination": "10.80.0.1:443",
  "service_id": "model",
  "operation": "generate",
  "request_id": "gateway-generated-request-id",
  "model": "entitled-model"
}
```

Data adapters provide `database` or `bucket`/`object_key` as appropriate. The
trusted adapter derives these values from the actual protocol request and uses
the kernel-derived destination. It must authorize every model request, SQL
statement and object operation, including requests reusing an established
connection. The response contains immutable session/turn/epoch/grant attribution,
policy digest, expiry and typed service restrictions. Denial is 403, conflicting
request identity is 409, and unavailable authorization is 503. All fail closed.
Bodies are bounded and unknown or trailing fields are refused.

`POST /v1/finish` accepts `backend_id`, `request_id`, `outcome` and nonnegative
`bytes`. The application supplies timestamps and preserves attribution from the
original request, even after the turn changes. Identical retries succeed;
contradictory finishes return 409.

## Revocation and shutdown

A new `Flows` registry starts with no active bindings. Reconciliation activates a
binding revision. Register an authorized request before upstream I/O and use the
returned expiring context for its stream/query. A duplicate active request ID is
refused.

Revoke marks the binding unavailable under the same lock as flow admission,
cancels all affected contexts and waits for every flow's `Finish` acknowledgment.
Adapters call `Finish` only after upstream activity has stopped. A timeout is
unresolved teardown, not successful revocation. Revoked IDs are tombstones and
cannot be activated again. Closing the registry prevents new bindings and drains
existing work.

These behaviors have contract/race tests, including real Unix peer credentials
and direct TCP listener refusal. Full real-Khora routing, external adapter
stream/query/signing behavior, upstream DNS/redirect policy and deployment UID
checks remain separate integration acceptance requirements.

## Application-to-gateway control

`ControlHandler` provides the reverse control direction inside a protocol gateway.
Serve it on a protected `ListenControl` Unix listener with `PeerContext`; configure
the application UID, gateway ID, protocol (`model_http`, `doris_mysql`, or
`obs_http`) and a positive maximum operation timeout. It uses the gateway's
actual flow registry. A separate registry that does not track upstream work
cannot acknowledge teardown for that work.

`GET /v1/info` returns version `1`, gateway `id`, `protocol` and a random process
`instance`. Every response includes `X-Harness-Gateway-Instance`. Mutations require
that same header from the caller and reject a different or missing instance with
412 before modifying the registry.

| POST endpoint | JSON body | Successful result |
|---|---|---|
| `/v1/bindings/activate` | `{"binding_id":"binding","revision":1}` | Binding revision registered |
| `/v1/bindings/revoke` | `{"binding_id":"binding"}` | Binding tombstoned and all its upstream work stopped |
| `/v1/grants/revoke` | `{"grant_id":"grant"}` | Grant tombstoned and all its upstream work stopped |
| `/v1/drain` | `{}` | Registry closed to admission and all upstream work stopped |

Success is exactly 204. Requests are bounded to 4 KiB; unknown fields, trailing
JSON, irrelevant fields and query parameters are rejected. Failed operations
return 503, expired drain deadlines return 504, and activation of a revoked
binding returns 409. Cancellation alone never proves that upstream work stopped.

`NewRemote` connects only to its configured absolute Unix socket, verifies the
server's kernel peer UID, and pins the handshake ID, protocol, version and process
instance. The server independently verifies the application's kernel UID. HTTP
proxy environment variables and redirects are not used. Timeouts, lost replies,
unexpected status codes or instance mismatches leave the operation unresolved.
Identical revocation retries can settle a lost successful reply against the same
instance. A restarted gateway cannot acknowledge that the previous process's
work stopped; the client returns `ErrGatewayRestarted` and requires explicit
reconciliation. Constructing a new client only establishes the new instance's
identity; it does not prove termination of the old one.

`NewRemotes` requires an explicit set of up to sixteen unique gateway controls.
Its route and grant revokers attempt every gateway concurrently and succeed only
after every gateway acknowledges. An empty set is an error. Client `Close`
releases idle control connections; gateway shutdown uses explicit `Drain`.

These control adapters have real Unix socket and race tests for peer rejection,
instance replacement, timed-out drains, lost replies and multi-gateway teardown.
The executable selects these clients through `HARNESS_KHORA_DEPLOYMENT`.
Upstream model/Doris/OBS compatibility is not established by these control tests;
the external protocol gateways must implement the documented contract.

## Durable synthetic destination allocation

`ReserveEgressCompilation` implements the `egress.Allocator` port for planned
executions. It requires the current product owner, session/environment identity,
expected environment fence, target epoch, typed policy/catalog, explicit gateway
selection for every service, and the deployment's synthetic/protected ranges.
Initial environments plan epoch 1; retained environments must be paused and plan
the next epoch. Running placements, unresolved lifecycle/recovery work, retired
sessions and blocked engine identities cannot receive fresh allocations.

The store pins one canonical private IPv4 range (`/8` through `/30`) to each
engine store identity. It reserves one address per service, excluding the range's
network and broadcast addresses. All ports on an allocated address remain
consumed. Preexisting route rows, including revoked ones, are skipped. The pool
cursor, immutable execution allocation and dormant compiled route rows commit in
one transaction. Failed compilation, route persistence or range exhaustion rolls
back the entire allocation. Exhaustion is `egress.ErrTupleExhausted`; it never
recycles old destinations or expands the range implicitly.

Retries with the same intent return the same bindings and compilation. A changed
session, environment fence, target epoch, policy, gateway or range fails. Binding
IDs are stable for the engine/execution/service identity. Revocation leaves the
allocation and route tombstones intact. After a store adopts managed allocation,
new direct route registrations must match its recorded reservations; exact
replays of historical registered rows remain valid without reviving authority.

Allocation is dormant: it does not configure Khora, activate gateway listeners,
start a process or issue a turn grant. Tests cover its transactional connection to
execution readiness and typed per-turn authorization. The daemon still needs to
compose allocation with paused network replacement, config-volume publication,
remote route activation and native restart before production service access is
complete.
