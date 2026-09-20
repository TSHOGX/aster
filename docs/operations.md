# Development operations

Start the product with explicit configuration and a separate isolated Khora engine.
The daemon requires `HARNESS_KHORA_DEPLOYMENT` and fails if its ports, release plans,
identity or bounded service settings are invalid. Read [configuration](configuration.md)
and [database](database.md) before selecting development paths.

Use `orchestrator version`, `/healthz`, product logs and durable operation records to
inspect a development run. Do not infer readiness from an open process stream alone;
accepted execution bindings and engine identity/epoch checks remain authoritative.
Pending operations and retirement must retain their durable evidence for reconciliation.

`native-capture --control-db PATH --trajectory-root PATH --engine-endpoint URL` produces
immutable native archives with product capture receipts. The product run directory,
prepared uploads, share blobs and native archives are application-owned host data;
session workspaces and Pi homes are accessible only through Khora.

Production release tooling has been removed from this development branch. Production
migration and deployment acceptance will be handled together after branch completion.
No production upgrade or deployment procedure is defined here.
