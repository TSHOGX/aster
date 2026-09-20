# Product database

`internal/store/schema.sql` is the complete current development schema. The store
initializes an empty development database transactionally. Existing files must match
the exact schema fingerprint, including constraints, indexes and triggers. Validation
opens them read-only before opening a writer; mismatched schemas are rejected.

There is no migration command, history catalog, schema stamp, database split tool,
or intermediate-branch upgrade path. Start with a fresh development database when
this schema changes. Never point a development binary at production data.

The database holds sessions/messages, turn and driver state, opaque Khora environment
and storage bindings, operation intents, execution grants, bridge journals/projection
positions, native capture requests/receipts, workspace maintenance, sharing,
authorization, schedules and notifications. It contains no embedded runtime allocation,
network profile, process checkpoint, host driver-home, Memory or Evolve tables.

WAL, one writer connection and a bounded reader pool support short transactions.
Remote I/O happens outside those transactions and is settled under owner/epoch/fence
checks. Immutable native archives and public share blobs are product-owned files;
their receipts/references are persisted in the database.

Production migration and deployment acceptance are deferred together to a separate
project after the branch is complete. Production Memory/Evolve data will be archived
separately, never migrated into this schema.
