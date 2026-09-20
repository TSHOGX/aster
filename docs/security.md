# Security boundaries

The HTTP server authenticates users and checks ownership or explicit read grants for
sessions, messages, workspaces and shares. Public links serve immutable whitelisted
snapshots. Browser requests use the same-origin frontend proxy with origin checks;
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
