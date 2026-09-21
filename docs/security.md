# Security boundaries

The HTTP server authenticates users and checks ownership or explicit read grants for
sessions, messages, workspaces and shares. Public links freeze a conversation boundary and serve immutable whitelisted
file snapshots. Browser requests use the same-origin frontend proxy with origin checks;
role checks protect administrator APIs. Untrusted guest output is sanitized before
public event delivery.

Khora provides sandbox isolation. Guest root is not an application authorization
boundary. Upstream credentials stay in trusted gateways; identity and service authority
come from accepted product bindings, epochs, grants and peer-authenticated gateway
contracts. Guest-provided source addresses or filesystem contents confer no authority.

Workspace requests use confined engine APIs and retained writer/read obligations.
The application does not read session workspace or Pi-home host paths. Prepared uploads,
share blobs and native archives are separate product-owned stores with bounded access.
A failed or ambiguous remote operation cannot release its durable fence prematurely.

The database accepts only the current development schema. Production compatibility,
upgrades and deployment acceptance are outside this phase. Tests must continue to cover
authorization, cross-session denial, replay, interruption and ownership changes.

## Authentication and the frontend proxy

With no configured users and no `HARNESS_LAB_PASSWORD`, the daemon runs in open
mode as `lab`; administrator routes remain denied. With login enabled, cookies
are HttpOnly and their expiry matches the server-side login session. Invalid or
revoked sessions return 401; an unavailable auth database returns 503.

Forwarded protocol/IP headers are trusted by default. For direct local access,
set `HARNESS_TRUST_FORWARDED_PROTO=false` and `HARNESS_TRUST_PROXY_HEADERS=false`.
When using a trusted reverse proxy, it must overwrite the forwarded identity
headers. Cookies use SameSite=None only with Secure on HTTPS; plain HTTP uses
SameSite=Strict. The frontend checks Origin hosts on writes before stripping the
header upstream. Direct daemon routes do not supply that frontend origin check.

The frontend permits framing (`frame-ancestors *`). Its write-origin check does
not prevent a user from being tricked into clicking controls in an embedded app.
Artifact previews have their own content restrictions. Current header behavior is
in [next.config.mjs](../frontend/next.config.mjs) and
[orchestrator-proxy.ts](../frontend/lib/orchestrator-proxy.ts).

Engine HTTP is restricted to loopback IP literals. HTTPS requires a CA and client
certificate/key; both daemon and capture use the same engine connection loader.
Gateway control uses kernel peer identity and process-lifetime fencing rather than
browser cookies. See [gateway contracts](gateway-contract.md).
