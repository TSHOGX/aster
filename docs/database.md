# Product database

The canonical SQLite schema is [schema.sql](../orchestrator/internal/product/schema.sql).
The current fresh lineage has schema version 2 and an exact compiled DDL
fingerprint. `product.Open` initializes that schema atomically on a new database
under an exclusive path/alias lock. An existing database must match both its
version and fingerprint before a writer opens. Application startup has no
migration chain or data backfill. The exact-source queued-prompts offline
converter is separate from the Store and operates only on a quiescent copy.

Application startup validates pending activation against the measured executable
through a read-only connection under the same lock. Only the selected replacement
can acquire authority; a rejected candidate cannot change schema or ownership.
One application owns all writes; operator control commands use that owner.
Credentials are an independently managed atomic YAML registry, not a second
product database.

Projects, saved profiles, owned resources and lifecycle operations describe
parent authority. Sessions and prompt rows own immutable input and queue policy;
`execution_runs` owns native execution status; `input_deliveries` owns exact
acceptance/consumption/removal evidence; `control_receipts` owns idempotent
admission results. Messages, commands, incarnations and native captures retain
their separate evidence. Derived conversation segments group consumed input and
following output without changing the native run. Terminals and their incarnations describe retained
PTYs. Activity rows account for admission and cleanup blockers. Events and bounded
projections drive browser recovery. Files operations preserve intent and uncertain
outcomes. Schedules/runs, reader grants, shares/files, notifications, auth sessions
and immutable content publications complete the product state.

Transactions serialize durable intent before remote side effects. Original IDs,
owner fences, revisions and postconditions govern settlement. A result from a
superseded owner or placement cannot update the active incarnation. Separate
Session control revisions are refresh markers. Prompt mutations compare a
prompt-local revision; Steer/Stop compare the exact run/incarnation/owner fence.

The database is not the engine control plane and does not store an alternate
copy of its internals. It saves public engine identities needed to observe and
reconcile resources. It does not grant browser clients raw engine handles.

Retention bounds event/journal projections and payload history; explicit child
and Project retirement purge private rows and host/guest artifacts after evidence
permits cleanup. Tombstones retain identity and deletion outcome without keeping
conversation payloads. Shared content publications remain separately owned.

Schema initialization commits the DDL, initial release singleton and version in
one transaction. Current-format databases reopen without conversion. Unknown or
modified DDL is refused without repair. The source identity index is populated by
current ingestion transactions, never by a historical backfill.

For a current installation, a coordinated database/volume backup must include
all retained application artifacts. A deliberately reset installation starts
fresh. The queued-prompts format change instead has a separately qualified
offline conversion path documented in [queued-prompts-release.md](queued-prompts-release.md);
neither path runs automatically when a Store opens.

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

Independent gateway authority uses `service_gateways` and `service_request_slots`.
An audience has one registered process instance and at most 256 reusable slots;
all audiences share a 256-active-request ceiling. Closed slots retain sequence
numbers to reject delayed acquisition and cleanup replies. Credentials and
request bodies are never stored in these tables. Active slots are indexed release
blockers even after owner restart or turn completion. They have no expiry: only
acknowledged forwarding settlement or a replacement gateway that has acquired the
exclusive lifetime lock can retire them. Registration uses generation CAS.


Candidate inspection obtains the compiled schema fingerprint from an in-memory
schema construction. It never inspects or mutates the live product database.
The application's startup identity and candidate CLI therefore agree even while
the current owner holds the exclusive Store lock.


The release singleton records mode, compatibility/schema digests and the pinned
gateway registration snapshot. `owner_transferable_turns` identifies running turns
with durably applied original prompts, active Projects and running incarnations.
`owner_handoff_blockers` retains every other release blocker, including requests
whose turn or grant no longer qualifies. Changes to retained prompt/turn/session,
grant/request authority or gateway registration revoke a pending owner-ready
checkpoint transactionally. The full `release_blockers` view remains authoritative
for quiescent releases. Operator status reports both sets in owner mode.

An owner-mode drain, ready or activating state requires the exact compiled schema
and deployment identity before any writer opens. An unmeasured Store open cannot
bypass that condition. Completion requires the selected executable. Replacing a
pinned gateway additionally requires full quiescence before completion.

File mutation rows distinguish volume authority (`volume_id`, `staging_path`)
from guest process authority. `project_resources.storage_key` records workspace
creation intent before remote allocation without marking compute occupied.
The canonical DDL contains the final admission triggers, indexes and blocker
views directly. Migration-only read-command retirement state has been removed.

The network-defaults singleton stores only direct rules, resolver order, content
revision and acceptance time. A lifecycle operation records its selected target
in durable request JSON. The `network` resume phase commits verified binding
revision and effective profile together before advancing to `sandbox`, then
`reachability`; admission remains closed until projection/readiness succeeds.
An uncertain PATCH is reconciled against the whole original/target binding.
