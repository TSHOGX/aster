# Product database

The core SQLite schema is [schema.sql](../orchestrator/internal/product/schema.sql).
Version 2 adds [conversation display storage](../orchestrator/internal/product/display_schema.sql);
version 3 adds [projector queues and ordering](../orchestrator/internal/product/projector_schema.sql).
`product.Open` validates and migrates known schemas while holding an exclusive path/alias
lock. One application owns all writes; operator control commands use that owner.
Credentials are an independently managed atomic YAML registry, not a second
product database.

Projects, saved profiles, owned resources and lifecycle operations describe
parent authority. Sessions, turns, messages, commands, incarnations and native
captures describe Agent work. Terminals and their incarnations describe retained
PTYs. Activity rows account for admission and cleanup blockers. Events and bounded
projections drive browser recovery. Files operations preserve intent and uncertain
outcomes. Schedules/runs, reader grants, shares/files, notifications, auth sessions
and immutable content publications complete the product state.

Transactions serialize durable intent before remote side effects. Original IDs,
owner fences, revisions and postconditions govern settlement. A result from a
superseded owner or placement cannot update the active incarnation. Separate
Session control revisions avoid making Stop/Steer stale on every token.

The database is not the engine control plane and does not store an alternate
copy of its internals. It saves public engine identities needed to observe and
reconcile resources. It does not grant browser clients raw engine handles.

Retention bounds event/journal projections and payload history; explicit child
and Project retirement purge private rows and host/guest artifacts after evidence
permits cleanup. Tombstones retain identity and deletion outcome without keeping
conversation payloads. Shared content publications remain separately owned.

Startup recognizes the exact original unversioned core schema and numbered
supported versions using their complete DDL fingerprints. Unknown versions or
modified DDL are rejected before a writer is opened. Known upgrades run atomically
before readers/workers start; DDL and `PRAGMA user_version` commit together.
Interruption before commit rolls back the whole upgrade. New databases run the
same ordered schema steps. Migration copies source identity metadata once in
original ingestion order; raw payloads and command evidence remain untouched.
The source identity index then appends inside each ingestion transaction.

Back up the database with SQLite's backup mechanism or `VACUUM INTO` while the
exclusive owner is stopped, and rehearse restoration to a separate path. Do not
copy just the main database file while WAL writes are active. Once the current schema has
accepted writes, rollback requires a schema-compatible executable. The original
unversioned executable cannot read the new schema. Restoring a pre-upgrade backup
over new accepted writes is not a supported rollback. Deployment must first close
and drain admission; the release gate is still under implementation.

The background display projector advances bounded session batches and serves the
versioned view API. Browser cutover is still in progress. Display tables do not
change execution state or ACK semantics. Inspect live state through operator
diagnostics; direct writes bypass authorization and reconciliation.
