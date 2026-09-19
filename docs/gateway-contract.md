# Gateway ingress and authorization

`internal/gateway` provides trusted ingress and flow-lifetime primitives. It is
not an upstream HTTP, MySQL or OBS implementation. Production protocol adapters
must compose these primitives with their existing credential and protocol logic;
the main application does not yet serve the new control contract.

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
