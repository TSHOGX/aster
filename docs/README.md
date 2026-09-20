# Documentation

These references describe the Khora-only development branch. Source and tests are
canonical; production deployment readiness is not claimed.

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
| [Drivers](drivers.md) | Pi and shell contracts |
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
