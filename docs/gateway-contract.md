# Project service gateways

A Project profile declares all direct and service reachability at creation.
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

Run the composed application under a non-engine host UID and configure Khora's
`--via-uid` to that same UID. The engine's second-hop floor blocks loopback,
link-local and sibling-sandbox destinations from that UID. Consequently the
application's engine endpoint must be a non-loopback authenticated TLS endpoint,
and frontend/model upstreams must use reachable non-loopback addresses. The
private application and operator APIs use Unix sockets. Do not weaken the engine
floor to make a loopback upstream reachable.

An operator can terminate engine mTLS at a trusted local reverse proxy forwarding
to the engine's loopback API. The application requires the CA and its client
certificate/key. The gateway's upstream should likewise use TLS whenever traffic
leaves the trusted host network. The isolated release test exercises mTLS and the
actual unprivileged combined application.

All processes inside one Project share a Linux trust domain. Guest root can read
a sibling Session's application grant. Grants provide request attribution and
host secret custody, not mutually hostile Session isolation. Put mutually
untrusted work in separate Projects.

## Protected authority protocol

`POST /gateway/v1/{registration,register,acquire,check,release}` is available only
on the mode-0600 application control socket. It is not a browser or guest API.
The owner limits audiences and capacity from configuration. Registration is a
privileged startup action requiring the gateway's exclusive lifetime lock; it is
never automatic recovery by a request handler. Generation CAS rejects delayed
registrations from a replaced process.

The remote authority client records each acquisition in a bounded durable slot.
Authorization and admission share the database writer transaction. An admitted
turn may continue requesting service during `draining`; `ready` and `activating`
reject new requests. Checks preserve original attribution and perform no writes.
Each successful response permits existing forwarding for at most five seconds
from the check's start, measured with the gateway's monotonic clock. Checks run
every 100 ms, with RPCs bounded to one second and to the remaining continuation
window. Confirmed denial cancels immediately on receipt; unavailable authority
never extends the deadline. New requests receive 503 during an authority outage.
There is no reconnect or silent upstream resubmission.

Release acknowledgment follows the end of forwarding. Lost acquisition replies
also require cleanup; closed slot sequence numbers prevent delayed acquisition
from resurrecting a released request. Eight bounded cleanup workers retry while
retaining the affected slots, so an owner outage cannot create unbounded cleanup
queues. Unacknowledged slots remain release blockers across owner restart. The
protocol is locally tested with controlled streams and faults; the default
application still hosts the gateway in its own process pending lifecycle
extraction. This does not yet establish independent process or real-provider
continuity.
