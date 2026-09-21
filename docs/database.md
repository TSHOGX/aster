# Product database

The fresh SQLite schema is [schema.sql](../orchestrator/internal/product/schema.sql).
`product.Open` creates and validates it while holding an exclusive path/alias
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

Use a fresh schema. There is no production migration path, old-schema importer or
second implementation selected by a configuration flag. Inspect live state through
operator diagnostics; direct writes bypass authorization and reconciliation.
