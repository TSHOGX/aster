# Documentation

These English-language documents describe the checked-in implementation, not a
roadmap or a particular deployment. Source and tests resolve implementation
ambiguities. Change the relevant reference when behavior changes.

| Document | Purpose |
|---|---|
| [Architecture](architecture.md) | Core components, boundaries and data ownership |
| [Deployment](delivery/operations.md) | Release download, local configuration, upgrade and rollback |
| [Operations](operations.md) | Routine diagnostics, credentials and persistent data |
| [Configuration](configuration.md) | Configuration resolution, runtime paths and optional integrations |
| [Development](development.md) | Isolated source development without production assumptions |
| [Control plane](control-plane.md) | State, persistence, recovery and authorization |
| [Session lifecycle](session-lifecycle.md) | Session/generation separation and cleanup invariants |
| [Sandbox runtime](sandbox-runtime.md) | OCI rendering, mounts, networking and runsc lifecycle |
| [Sandbox image](sandbox-image.md) | Rootfs build inputs, manifest and bridge process |
| [Drivers](drivers.md) | Pi and shell deployment contracts |
| [HTTP API](api.md) | Routes, payloads, streams and sharing |
| [Frontend](frontend.md) | Workbench data flow and event handling |
| [Runtime synchronization](runtime-state-synchronization.md) | Revisioned UI projection |
| [Memory and knowledge](agents-kb.md) | Optional extraction subsystem, KB approval and external evaluation boundary |
| [Notifications](notifications.md) | Persisted notifications and delivery |
| [Security](security.md) | Isolation, identity, network and credential boundaries |
| [Database operations](database.md) | Explicit migrations, local catalogs and legacy split tooling |
| [Verification](verification.md) | Source checks, release artifact tests and local acceptance |
| [Release process](release-process.md) | Tags, hosted builds, release notes and publication |
| [Public/private boundary](delivery/privacy.md) | Sanitization, local handoff and history review |

## Where other information belongs

- Future work and unimplemented features:
  [Issues](https://github.com/TSHOGX/agent-runtime-platform/issues). The existing
  [core roadmap](https://github.com/TSHOGX/agent-runtime-platform/issues/3) remains
  the planning reference. No local proposal or roadmap archive is maintained.
- Version announcements and one-off upgrade caveats:
  [Release notes](https://github.com/TSHOGX/agent-runtime-platform/releases).
- Host-specific instructions, deployment results and audit evidence: a private
  operations system or ignored local `private/` directory, never public docs.
- Source-test fixtures are synthetic and versioned with tests. Historical schema
  fixtures used by migration regression tests are retained as executable test
  inputs, not user-facing upgrade documents.
