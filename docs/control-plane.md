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
