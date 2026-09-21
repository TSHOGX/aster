# Documentation

These references describe the Khora-only development branch. Source and tests define current behavior. Historical test results retain their
actual execution revisions; production deployment readiness is not claimed.

Start with [development](development.md) for an interactive daemon,
[template building](sandbox-image.md) and the [isolated harness](../dev/README.md)
for real-engine tests, or [operations](operations.md) to diagnose a running instance.

| Reference | Purpose |
|---|---|
| [Architecture](architecture.md) | Product, engine and storage ownership |
| [Development scope](development-scope.md) | Branch boundary and separate future production project |
| [Development](development.md) | Isolated local workflow |
| [Configuration](configuration.md) | Product settings and required Khora composition |
| [Database](database.md) | Exact current schema; no migrations |
| [Control plane](control-plane.md) | Authority, durable work and reconciliation |
| [Recovery](recovery.md) | Durable operation discovery, startup and interruption recovery |
| [Session lifecycle](session-lifecycle.md) | Retained storage and turn recovery |
| [Sandbox runtime](sandbox-runtime.md) | Public Khora API boundary |
| [Sandbox image](sandbox-image.md) | Template and manually maintained assets |
| [Drivers](drivers.md) | Pi Agent driver contract |
| [HTTP API](api.md) | Product routes and streams |
| [Frontend](frontend.md) | Workbench behavior |
| [Runtime synchronization](runtime-state-synchronization.md) | Revisioned client state |
| [Gateway contract](gateway-contract.md) | Peer identity, grants and revocation |
| [Notifications](notifications.md) | Inbox and audience checks |
| [Security](security.md) | Trust and authorization boundaries |
| [Development evidence](development-evidence.md) | Executed fault paths and finite workload measurements |
| [Verification](verification.md) | Development correctness and real integration |
| [Operations](operations.md) | Development diagnostics |
| [Public/private boundary](delivery/privacy.md) | Private configuration and evidence |

Machine-specific plans and raw evidence stay in ignored `private/`. Historical
planning reports do not add compatibility obligations to the current scope.

## Maintaining these references

When behavior changes, update the relevant API/configuration reference and its
setup commands together. Verify endpoint methods/status codes against handlers,
configuration names/defaults against loaders, and browser behavior against the
actual capability branches. Keep observed behavior distinct from intended cleanup.

Run `python3 scripts/check-public-docs.py` and `git diff --check` from the repository
root. The documentation checker validates local link targets, reference-language
conventions and the private boundary; it does not verify command behavior, section
anchors, API semantics or historical evidence. Review those explicitly. Use
[verification](verification.md) for code or runtime changes.

Record tests at the revision and dirty state emitted by the harness. Publish sanitized
synthetic summaries rather than copying raw logs; a later docs-only commit is not a
new execution result. See [evidence](development-evidence.md) and
[public/private boundary](delivery/privacy.md).
