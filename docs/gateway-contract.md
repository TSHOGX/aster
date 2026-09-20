# Gateway ingress and authorization

`internal/gateway` provides trusted ingress and flow-lifetime primitives. It is
not an upstream HTTP, MySQL or OBS implementation. Production protocol adapters
must compose these primitives with their existing credential and protocol logic;
the Khora daemon mode serves the new authorization contract and selects remote
control adapters.

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
