# Development operations

Start the product with explicit configuration and a separate isolated Khora engine.
The daemon requires `HARNESS_KHORA_DEPLOYMENT` and fails if its ports, release plans,
identity or bounded service settings are invalid. Read [configuration](configuration.md)
and [database](database.md) before selecting development paths.

Use `orchestrator version`, `/healthz`, product logs and durable operation records to
inspect a development run. Do not infer readiness from an open process stream alone;
accepted execution bindings and engine identity/epoch checks remain authoritative.
Pending operations and retirement must retain their durable evidence for reconciliation.

`native-capture --control-db PATH --trajectory-root PATH --deployment PATH` produces
immutable native archives with product capture receipts. The product run directory,
prepared uploads, share blobs and native archives are application-owned host data;
session workspaces and Pi homes are accessible only through Khora.

Production release tooling has been removed from this development branch. Production
migration and deployment acceptance will be handled together after branch completion.
No production upgrade or deployment procedure is defined here.

`orchestrator diagnostics --db /absolute/path/control.db` reads current-schema
product state without writer authority, including while the daemon is stopped.
Authenticated administrators can use `GET /api/admin/diagnostics` for the same
queues plus live worker and gateway status. Ordinary users cannot access it.

Diagnostics include oldest pending operation/turn/drain/capture/projection age,
worker observation and success times, stale-worker flags, recorded pipeline errors,
original operation identities, active capacity and reserved tuple capacity. Storage
values distinguish configured retained capacity, logical snapshot/archive bytes,
workspace inventory and allocated database pages; they are not a claim of exact
physical CoW usage. An old owner cannot refresh the new owner's worker health.

A detached process stream requests immediate fenced adoption instead of waiting for
lease expiry. Loss of a stream is still not proof that the process died. Optional
turn continuation is one bounded maintenance task over committed failed turns;
backoff survives restart and newer user work always supersedes old failures.
