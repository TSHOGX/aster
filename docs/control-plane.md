# Control plane

The Go orchestrator owns durable session state, runtime allocation, bridge events,
authentication, files, sharing and user schedules. See [API](api.md) for wire
contracts and [runtime](sandbox-runtime.md) for sandbox mechanics.

## Startup and shutdown

Startup resolves and validates explicit configuration, takes the process owner
lock and canonical database-path locks, opens the control/memory stores, records
the owner and constructs runtime/event/artifact services. Managed mode verifies
schema without DDL; offline migrations use the separate database command.

Runtime recovery reconciles expired resources before ordinary work. Background
workers handle owner heartbeat, artifact watching, idle release, bridge polling,
maintenance and the independent core scheduler. Optional memory extraction starts
only when configured; its lifecycle is described in [memory and knowledge](agents-kb.md).

The HTTP API and Unix-socket proxy-correlation server shut down under a bounded
deadline. Application shutdown closes long-lived streams before HTTP shutdown;
forced connection close is the fallback. A shutdown does not itself promise a
consistent backup of independently running external writers.

## Persistence groups

| Group | Main records |
|---|---|
| Identity | users, auth sessions, roles, feature flags, admin audit |
| Conversation | sessions, turns, messages, traces, feedback, events and search indexes |
| Files/sharing | artifacts, workspace records, session shares, share links/file blobs |
| Runtime | generations, network/runtime profiles, allocation/resources, driver homes/state, sandbox contracts |
| Scheduling | agent schedules and durable schedule runs |
| Deployment | owner and schema-migration records |

The control database is SQLite in WAL mode with one writer connection and a
reader pool. High-frequency reads use the reader pool; output envelopes can be
committed in batches. Durable events are committed before publication to the
in-memory hub. The hub is a live transport, not a replacement for stored events.

Memory tables are physically separate and do not hold cross-file SQLite FKs.
`internal/controlsource` provides the ingestion boundary.

## Turn and bridge contract

Enqueue creates a durable turn. For current user turns, the transcript message is
materialized when dispatch starts rather than immediately duplicating queued
input in both transcript and queue. Historical messages retain their existing
representation; migrations do not guess missing message/turn associations.

A sandbox claims a turn, acknowledges start, emits output and acknowledges its
terminal state. Generation identity and leases fence each transition. Terminal
turn output is rejected as permanent so stale events do not indefinitely block a
bridge polling pass.

Stop aborts the active Pi turn and parks the remaining queue. A parked turn has
`auto_dispatch=0`; it can be explicitly sent, potentially after cold-starting a
released sandbox. Steered input records its destination turn and does not create
a competing live turn. See [drivers](drivers.md).

## Recovery and resource lifecycle

Maintenance stages have independent deadlines and do not overlap themselves.
Cold fallback has its own worker so blocked cleanup cannot indefinitely starve
queued work. Runtime exit callbacks and heartbeat checks retire dead generations
and reconcile session projections.

Generation cleanup is fenced by its recorded identity and must prove resource
absence before committing destruction. Persistent user data is not a generation
resource. See [session lifecycle](session-lifecycle.md).

## Core scheduled tasks

User schedules and runs persist across restart. A dedicated ticker and worker pool
handle due triggers, bounded concurrency, retries, timeout and recovery. Results
can use a new session or an allowed existing agent session. Disabled/archived
schedules stop new dispatch, and stale cron windows are skipped according to the
misfire grace instead of replayed without bound.

`scheduler.enabled=false` suppresses new cron triggers; it does not by itself
stop recovery of queued/running schedule rows. A feature flag is a capability
indicator, not a global stop-write barrier. Deployment drain/background hold and
writer shutdown have separate roles.

Memory extraction or an external nightly job is not a core user schedule simply
because it also runs on a timer.

## Authorization and mutable capabilities

Credential YAML supplies password hashes and request roles. Persisted users are
a projection, not a second credential authority. Normal routes enforce session
ownership/sharing; `/api/admin/*` has explicit administrator middleware. Open or
legacy lab access does not imply admin access.

Feature flags and admin audit records exist, but a flag's presence does not prove
a complete product feature exists or that unrelated routes consult it. Read
actual handlers when assessing authorization.

Proxy request start/finish travels over an authenticated Unix socket. Source IP,
active generation, active turn and sandbox contract determine authorization;
sandbox-provided diagnostic identity cannot grant access.

## UI state

Runtime snapshots read lifecycle and turns consistently and carry a revision
covering all dependencies. HTTP and SSE snapshots have the same semantics;
clients reject stale revisions and reconcile after gaps. See
[runtime synchronization](runtime-state-synchronization.md).

## Durable bridge ingress

Accepted bridge output and completion records are also retained in
`bridge_ingress`, independently of SSE event retention. Their identity includes
runtime generation, turn, attempt and source sequence (or completion marker).
Contradictory output replay fails closed even after the public event has been
pruned. The payload digest covers the event type, stream and JSON bytes.

`bridge_projections` stores a versioned consumer checkpoint. The store's
projection transaction checks the preceding checkpoint and next source record,
then writes messages, message traces and source-to-message identities together.
A failed write rolls back the entire projection; a stale worker cannot allocate
a second message. Ingress collection is not enabled: source records remain
available until all consumer and recovery obligations can be established.

Existing managed databases require the explicit offline `db migrate` workflow
before starting this schema. Both previously stamped and unstamped reviewed
baselines are supported. Migration preserves existing messages and traces and
does not reinterpret historical SSE events as new producer input.

The maintenance worker consumes this journal on every poll, including records
whose original post-commit callback never ran. A checkpoint includes pending
assistant text, tool traces, driver identity and normalizer version. Source
capture timestamps drive trace timing, and unfinished tools have deterministic
ordering. Message rows, traces, stable source ordinals, replayable `agent.message`
events and the next checkpoint commit in one transaction. Live notifications
follow commit; reconnecting clients can replay the durable message event.

Before completing a bridge turn, the application durably records its completion
marker and projects through it, including any remaining partial response. The
store refuses completion and driver-state advancement while that marker or its
preceding output is unprojected. New output after the marker is rejected. The
ordered local carrier supplies the completion boundary; this is not yet a remote
process-stream protocol. Reconnect output watermarks come from retained ingress,
with historical event records used only for drained pre-upgrade turns. New
output must be contiguous. Projection errors retain their input for retry.

Bridge protocol processing uses a transport interface with receive, send and
acknowledge operations. The local queue is one carrier for that interface. The
host supplies the accepted session/generation scope from its binding record;
every incoming envelope must match before hello, claim, heartbeat, output or
completion handling can reach persistence. Batched output receives the same
check. Replies use the host scope, including replies rejecting a forged scope.
Carrier receipts remain separate from envelope and output identities, so a lost
acknowledgment can redeliver input without allocating a second journal record.

Generation-start ownership is coordinated by `internal/execution`. HTTP and
scheduled starts use the same lease guard while preparation and readiness run.
The guard renews the durable owner fence, cancels backend startup on renewal
failure, and cannot revive a failed attempt. Renewal calls are bounded by half
the lease TTL, capped at five seconds, so a blocked renewal cannot consume an
unrelated fixed timeout for short leases. Backend provisioning and readiness
mechanics remain in the existing lifecycle path.

The execution service also owns generation selection: reuse a usable execution,
refuse a transition already in progress, reserve a replacement for a failed
execution under the prior-generation fence, or claim retained state for resume.
A conflicting resume claim causes one session refresh and at most one retry.
The current backend adapter translates database statuses, allocation configuration
and driver-state tokens; HTTP and scheduler callers use the same service result.

## Retained environments and remote operation intents

The control store has additive backend, retained-environment and operation
records. Backend names are pinned to the engine's durable `store_id`; a changed
identity is refused without rewriting existing sandbox references. Environment
bindings are scoped to a session and backend identity and retain their desired
configuration independently of replaceable processes.

Each environment admits one unresolved operation. Reserving it advances a durable
fence and preserves its request bytes, request digest, remote idempotency key and
expected engine mutation revision. Pending, issued and uncertain outcomes keep
the lane occupied, including across restart. Reusing a key with different intent,
reusing a terminal key for a new attempt, or applying an old callback after a
successor advances the fence is refused. Only a reconciled successful outcome
can update observed sandbox identity, placement epoch and mutation revision.
These records do not activate execution or grant gateway authority by themselves.

The environment execution service reserves and records an issued operation before
calling the backend. Its repository calls finish before remote work begins;
a blocked remote call does not hold the database writer. A lost pause/resume
reply can be reconciled after a 412 only when current inspection establishes the
original operation's exact postcondition and epoch. It never refreshes an old
request's mutation revision. Other uncertain outcomes keep the lane occupied.
Capacity and invalid-request refusals are terminal; a generic 409 remains
uncertain because it may describe an in-progress idempotent operation. Successful
terminal replay returns the committed result without remote redispatch, and an
older operation cannot replay across a successor's fence.

## Process carrier framing and guest journal

The process carrier codec uses version 1 JSON records prefixed by byte `0x1e`
and terminated by newline, with an 8 MiB frame limit. JSON escapes those bytes
inside strings. A new prefix abandons an incomplete send, allowing a retry to
resynchronize without joining two partial commands. Go/Python interoperability
tests cover split boundaries, Unicode, control characters and integer precision.
Stream positions belong to an execution journal independently of Khora's process
cursor. The framing contract includes an unconditional sync/state exchange for
every attachment; an engine gap flag cannot replace that exchange.

The guest journal uses SQLite WAL with FULL synchronization and one producer lock
per execution. It commits records before returning them for publication, retains
head and acknowledged positions across reopen, and deletes only acknowledged
output. A host position below the retained boundary is an explicit gap. Command
IDs retain payload digests, and contradictory duplicates are refused. The default
logical journal capacity is 256 MiB; exhaustion fails instead of dropping records.
Filesystem quota remains the physical storage bound. The guest claim loop supports `--transport stdio` with this journal; the file
carrier remains the default during migration. The host process carrier performs
sync/state before accepting records, persists exact receipt digests and a
contiguous watermark before ACK, and requests replay after gaps. Receipt state
is bound to backend identity, execution, process handle and epoch. Stdio
completion declares its final output sequence; persistence refuses completion
or driver-state advancement with missing output or pending projection. A local
integration test runs the actual shell driver through framed stdin/stdout and
verifies retained completion data. Template/substrate integration remains a
separate acceptance gate.

## Engine event recovery

Engine events are retained in a separate inbox scoped by durable engine store
identity. Event ID and sequence deduplication, payload-digest checks, reconciliation
requests and cursor progress commit together. A contradictory replay rolls back
the whole page. Historical events never overwrite an environment's accepted
placement epoch or mutation revision; they schedule current inspection instead.

The Khora SDK discards typed bounds on a 410 response, so the adapter obtains a
fresh typed window with `Events(after=0, limit=1)`. The reconciler persists that
window before scanning known bindings, then replays from `earliest - 1`. A crash
repeats the scan and replay; after reaching the captured watermark, another scan
settles current state before resync completes. If retention overtakes recovery,
the new 410 starts another persisted resync window. This service is independently
tested and awaits wiring into the Khora execution worker; the existing local
maintenance path remains active.

## Workspace mutation admission

Local upload/edit writes reserve a durable session mutation before consuming the
content stream. The reservation checks session and generation state together
with queued/leased/running work. SQL constraints then reject turn admission,
parked-turn dispatch, generation allocation and session/generation transitions
while that mutation is active or uncertain. This closes the earlier gap between
an HTTP busy check and the actual file replacement.

File I/O runs outside the reservation transaction. Successful or definitely
failed replacement records its outcome and releases the fence. An uncertain
post-rename durability outcome retains the fence and its content digest for
reconciliation; elapsed time does not authorize another writer. Mutation
outcomes are fenced by operation identity and owner, and terminal outcomes cannot
be rewritten. Recovery must establish that the prior writer stopped before
settling an interrupted mutation. The remote helper is not yet wired into this
local mutation path.

Pi native-file validation now belongs to `internal/driverstate`, outside store
transactions. Before completion, the application validates canonical state,
opens the selected native source through the confined workspace reader and
computes its digest. It passes evidence bound to session, generation, turn,
owner, state digest and sandbox contract digest. Completion rechecks those values
alongside the existing driver-state CAS and owner lease; missing evidence or a
changed storage binding refuses advancement. Guest payloads cannot supply this
application-only evidence field. The turn remains active through validation, so
HTTP workspace mutation remains fenced. Launch validation uses the same source
validator. The store's driver-state codecs perform metadata validation only.

Native trajectory capture accepts a source stream and portable session identity.
It copies bytes once into a synchronized staging file, parses that same snapshot,
then publishes an immutable archive named by native identity and content digest.
The archive directory is synchronized before the existing raw-source and
trajectory records are updated. A native rewrite with the same byte count now
creates a new archive reference; unchanged bytes reuse the existing verified
archive. Older references remain readable. The local session enumerator is a
source adapter during migration; memory/evolve schemas and algorithms are
unchanged and require no engine client.
