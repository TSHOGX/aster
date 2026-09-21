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

## Triage and retained data

| Observation | Inspect and next action |
|---|---|
| Health responds but turns cannot start | Check admin diagnostics for engine identity, pending start/configure operations, gateway readiness and occupied capacity |
| Turn control or retirement returns 503 | The durable obligation may still be pending; inspect its operation/drain record and allow reconciliation before retrying the same action |
| Workspace write succeeded but files look stale | Refresh directory metadata; inventory can lag while maintenance/writer fences settle |
| Capture lag grows | Check that the separate worker is running with the same database/deployment, inspect `pipeline_failures` and `retry_after`, and verify the archive root is writable |
| Gateway socket was replaced | Check process-instance and pidfd evidence; a new socket does not prove the previous process exited |
| Engine store/epoch contradicts a binding | Restore verified original engine/storage state; do not rewrite product identities to bypass fencing |
| Tuple or storage capacity grows | Inspect `tuple_pools`, retained reservations and archive sizes; existing identities and user data are intentionally retained |

Capture accepts absolute paths for all three required flags. Its default poll is
5 seconds, batch timeout 2 minutes and per-item timeout 30 seconds. `--once` handles
one batch, at most one eligible version per session, and can finish with zero work
while sources await activation or retry deadlines; it is not a drain-all command.
Capture failures back off durably, starting at 30 seconds and capped at 8 minutes.

Keep the product database together with its engine store identity, retained engine
storage, share blobs and native archives. Product DELETE and daemon shutdown do not
reclaim user storage. Published content volumes must remain available while referenced;
deleting them independently can leave durable publication/idempotency records pointing
at missing objects. Test-substrate cleanup is separate from application retention.
