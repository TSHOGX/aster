# Project service gateways

A Project profile declares all direct and service reachability at creation.
Accepted direct/DNS defaults may replace those portions at a later resume boundary;
service routes and model host mappings remain bound to the Project. Running
placements are never rewritten by a network policy edit.
Direct tuples are usable by every Project process and carry no Agent authority.
Service tuples map a synthetic destination and optional stable hostname to an
explicit host gateway. Empty direct/service lists are supported and deny egress.
A child cannot rewrite this declaration or create a new service audience.

Each Agent incarnation receives a scoped application grant. For every model
request, the gateway verifies the live grant, Project, incarnation and admitted
turn in the product database. It replaces guest-supplied attribution headers with
host-derived Project/Session/incarnation/turn IDs. An idle Agent and a Terminal
have no admitted Agent turn and cannot obtain service authority by naming one.

The gateway holds the upstream API key from its configured environment variable.
It authorizes each request and periodically rechecks in-flight attribution. A
turn's completion or revocation drains only that request; sibling requests
continue. Stop admission rejects new requests but preserves the existing stream
until native cancellation settles, so abort cannot race a gateway disconnect. Request concurrency and observer queues are bounded.

Run the owner and independent gateway under the same non-engine host UID and
configure Khora's `--via-uid` to that UID. The engine's second-hop floor blocks loopback,
link-local and sibling-sandbox destinations from that UID. Consequently the
application's engine endpoint must be a non-loopback authenticated TLS endpoint,
and frontend/model upstreams must use reachable non-loopback addresses. The
private application and operator APIs use Unix sockets. Do not weaken the engine
floor to make a loopback upstream reachable.

An operator can terminate engine mTLS at a trusted local reverse proxy forwarding
to the engine's loopback API. The application requires the CA and its client
certificate/key. The gateway's upstream should likewise use TLS whenever traffic
leaves the trusted host network. The isolated release test exercises mTLS and the
actual unprivileged owner and gateway processes.

All processes inside one Project share a Linux trust domain. Guest root can read
a sibling Session's application grant. Grants provide request attribution and
host secret custody, not mutually hostile Session isolation. Put mutually
untrusted work in separate Projects.

## Protected authority protocol

`POST /gateway/v1/{registration,register,acquire,check,release}` is available only
on the mode-0600 application control socket. It is not a browser or guest API.
The owner limits audiences and capacity from configuration. A removed audience
loses new/continued authority, while its original registered gateway may still
acknowledge request settlement. Registration is a
privileged startup action requiring the gateway's exclusive lifetime lock; it is
never automatic recovery by a request handler. Generation CAS rejects delayed
registrations from a replaced process.

The remote authority client records each acquisition in a bounded durable slot.
Authorization and admission share the database writer transaction. An admitted
turn may continue requesting service during `draining`. Quiescent `ready` and
`activating` reject new requests. Owner-only handoff permits requests for retained
running/applied turns in those phases, under the same durable authority checks. Checks preserve original attribution and perform no writes.
Each successful response permits existing forwarding for at most five seconds
from the check's start, measured with the gateway's monotonic clock. Checks run
every 100 ms, with RPCs bounded to one second and to the remaining continuation
window. Confirmed denial cancels immediately on receipt; unavailable authority
never extends the deadline. New requests receive 503 during an authority outage.
There is no reconnect or silent upstream resubmission.

## Independent process and gateway updates

`orchestrator gateway --config FILE` starts a separately supervised process after
the owner is available. It binds the declared service listeners, obtains the
exclusive state-directory lifetime lock and registers each configured audience.
Owner/API shutdown does not touch its listeners, upstream connections or secrets.
Restart configuration supplies keys again through the gateway environment; no key
is persisted in product state. The gateway and owner must implement the exact current authority protocol;
unsupported peers cannot authorize or extend requests.

`gateway-status --socket STATE/gateway.sock` reports PID, active handlers, pending
settlements and authority failure/expiry/denial counters. A gateway update first
uses the application's admission drain. Then `gateway-stop` takes the same owner
and generation, obtains a fresh atomic readiness check from the owner and rejects
shutdown unless local forwarding and pending acknowledgments are both empty.
The process completes the control response before exit and keeps its lifetime
lock until all forwarding handlers return. Start the replacement gateway before
aborting the application drain to reopen admission. Signal termination or a crash
can interrupt streams; use the gated stop command for an orderly update. HTTP
streams are not resumed across gateway replacement.


Owner handoff requires gateway `owner_handoff_protocol: 1`, in addition to
authority RPC protocol 1. It pins the live gateway registrations (instance, generation and slot
capacity) reported by the protected status endpoint and checks them against the
Store in each transition transaction. Gateway status includes those identities;
no credential is returned. A new registration revokes pending owner readiness.
Owner-only readiness never permits gateway shutdown. If gateway replacement occurs
after owner activation, the selected owner requires full quiescence before
completing with the changed gateway.


## Reserved schedule platform route

The whole `/platform/` namespace (and bare `/platform`) is reserved locally on
every gateway, even when scheduling tools are disabled. Unknown versions, paths,
trailing slashes and missing handlers return a bounded local error. They never
reach model providers or receive provider credentials. Normal model requests
retain the existing proxy behavior.

Set `schedule_tools: true` only on a configured audience selected for platform
capabilities. POST `/platform/v1/schedules` supports `context` and `execute`;
execute has a fixed create/list/get/update/cancel allowlist. The 2 MiB strict JSON
envelope shares gateway request capacity and existing grant acquisition/release
accounting. Get history is capped at 20 rows, list metadata at 50 rows, and list
never contains prompts. The endpoint is derived from the declared service origin,
not a provider-specific `/v1/messages` URL.

The gateway forwards to `/platform-owner/v1/schedules` on the protected owner Unix
socket. It opens no product database. Guest identity headers are ignored. The
owner checks the immutable configured audience capability and rechecks the
registered gateway instance, slot sequence/open state, original grant, Project,
Session, incarnation, execution Run and owner snapshot inside the mutation
transaction. Fresh platform work requires a starting/running Run and its exact
expected ID. Stopping, completed, revoked, expired and stale-owner requests fail,
even when model-stream continuation remains valid. New mutations also require an
open release phase. Authorized exact receipt replay may survive release drain;
an idle old agent credential cannot inspect it.

Control status advertises `platform_protocol: 1` and a per-audience `schedule_tools`
capability map; enabled owner audiences must match both. Release compatibility includes
that protocol. An owner with scheduling enabled refuses handoff qualification
against a gateway lacking it. Install the prefix-reserving gateway through the
existing full-quiescence gateway replacement procedure before enabling runtime
tools. Owner-only handoff cannot install a changed schema or gateway protocol.
