# Operations

Use [release deployment](delivery/operations.md) for install/upgrade/rollback and
[database operations](database.md) for schema and backup procedures. This guide
contains only host-independent operational contracts.

## Identity and health

`orchestrator version` reports the build version and commit. `/healthz` additionally
reports deployment drain/background-hold state. Inspect the executable actually
running when adopting a legacy service; a checkout HEAD or directory name does
not prove its identity.

`harness-deploy inspect --config /private/deploy.json` reads the configured service
and executable. Its output can contain host identity and paths; retain it in the
private operations system. `status` reads recorded deployment operations.

A successful HTTP health check does not establish model connectivity, sandbox
launch success or historical-data integrity. Perform the target's configured
business smoke before opening user traffic.

## Service ownership

The orchestrator runs with the privileges required for runsc, network namespaces,
nftables and owned data volumes. The frontend runs as the configured unprivileged
account. Production model/data gateways and external evolution workers have their
own service ownership and credentials.

Service names, ports, TLS configuration, log locations and restart policies are
local configuration, not repository defaults. Query the appropriate systemd units
with `systemctl status <unit>` and `journalctl -u <unit>`; do not stop processes by
assuming every listener on a familiar port belongs to this deployment.

## Credentials and access

Manage users with `orchestrator user list|add|passwd|rm` and explicit configuration.
Use `--password-stdin` to avoid passwords in process arguments and shell history.
The credential file contains PBKDF2 hashes and reloads without a service restart.
A malformed replacement retains the last loaded credentials and logs an error.

Managed deployments require a nonempty valid credential file. Unmanaged open
mode exists for isolated development only. Configure TLS termination, trusted
forwarded headers and private backend/frontend listeners before exposing users.

## Availability and diagnostics

- Inspect turn, generation and resource state together. A released sandbox is not
  a deleted conversation, and a generation failure is not necessarily a terminal
  session failure.
- Check maintenance heartbeat progression, sandbox heartbeat age and bounded
  cleanup errors before attempting manual repair.
- Use the runtime projection to distinguish queued, parked, active and released
  work. Parked turns should not be counted as automatic pending work.
- Model authorization failures can arise from stale turn/generation identity,
  leases or proxy credentials; disabling correlation is not a repair.
- Avoid direct SQL state edits or deleting workspaces to resolve a stale runtime.
  Preserve logs and follow the generation-fenced cleanup/recovery path.

## Maintenance and data ownership

A maintenance page blocks new ingress but does not stop existing requests or
background writers. The deployment drain blocks new work while existing work
finishes; the separate background hold permits controlled smoke without starting
new automatic jobs. Inventory external processes as well as systemd services.

Persist backups outside the live data roots and check space on both backup and
database filesystems. A rollout rollback requires the relevant data snapshot and
matching program/rootfs; a new binary alone does not undo changed semantics.

The optional [knowledge jobs](agents-kb.md) have independent ownership. User Cron
Agent jobs belong to the core scheduler; memory extraction and an external
nightly/evaluation runner do not share that scheduler's lifecycle automatically.

## Private handoff

Keep per-host inventories, service drop-ins, credential references, maintenance
windows and local helper scripts outside public source. An ignored `private/`
folder can hold temporary handoff packages; transfer them through a private
channel and inspect references before deleting old deployment scripts.
