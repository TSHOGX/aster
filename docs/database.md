# Product database

The core SQLite schema is [schema.sql](../orchestrator/internal/product/schema.sql).
Version 2 adds [conversation display storage](../orchestrator/internal/product/display_schema.sql);
version 3 adds [projector queues and ordering](../orchestrator/internal/product/projector_schema.sql);
version 4 records [actual event retention frontiers](../orchestrator/internal/product/event_frontier_schema.sql);
version 5 adds [reusable reader bindings](../orchestrator/internal/product/reader_schema.sql);
version 6 adds [retention accounting and cleanup](../orchestrator/internal/product/retention_schema.sql);
version 7 adds [turn-scoped final-message lookup](../orchestrator/internal/product/navigation_schema.sql).
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
versioned view API used by the browser. Raw records are no longer reconstructed
inside HTTP requests. Display tables do not
change execution state or ACK semantics. Inspect live state through operator
diagnostics; direct writes bypass authorization and reconciliation.


File mutation rows record writes/mkdir/move/remove only. `workspace_readers` holds
one recoverable process identity per Project, bound to its original sandbox and
epoch. It stores neither a command outcome nor file bytes and creates no Project
activity. Warm reads do no product writes; startup and confirmed helper retirement
update only the binding. Existing uncertain mutations remain independently
recoverable and are never inferred from a reader result.


Writer admission has separate Stop, ordinary control, ingestion and background
capacity. Background retention/projection cannot consume reserved Stop queue slots;
waiting ingestion is bounded by both count and bytes. Admission rejection precedes
transaction execution. Unknown side effects remain unknown; callers must not
interpret arbitrary transport failures as those explicit rejections.

Derived replay retention removes bounded prefixes with revision/ordinal frontiers.
Current details and retained change-version details are pinned. Raw records, source
identity, native captures and uncertain command identities remain until their
existing explicit retirement authority permits deletion. Schema 6 removes obsolete
read command rows while retaining original helper identities for cleanup; no read
is replayed through a mutation dispatcher. Product events no longer include
per-token raw-record notifications.


Guest journal format 2 records each command result's sequence. A cumulative host
ACK can compact a resolved command's original payload, but never its digest,
identity or result. Uncertain commands and active prompts are excluded. The host
retains authoritative raw execution evidence independently of this guest
reclamation. SQLite FULL is the direct connection default and is verified; there
is no weaker durability path selected for reconnect recovery.
