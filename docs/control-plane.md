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

For Khora sessions, Stop and steering use durable turn-command rows and the
existing process carrier's serialized input writer. Commands name the session,
generation and original turn and retain the same ID after an ambiguous send.
The guest's command journal deduplicates them; scoped receipt acknowledgments
are persisted separately from driver completion. A receipt proves durable guest
acceptance, not exactly-once external tool execution or completed cancellation.

Stop atomically parks queued turns and revokes the active turn's grants, then
waits for the configured gateway grant revoker to drain established work before
delivering abort. It leaves the current turn running until its terminal journal
record settles cancellation, preserving output emitted during shutdown. Shell
uses the existing PTY interrupt primitive; Pi uses its native abort RPC. Steering
requires Pi. Its source prompt is atomically parked and associated with the
command before delivery. A guest-journaled `turn_control_result` retires that row
and records its transcript message and event together; a receipt alone does not.
Late results retain the original turn attribution and duplicate results are
idempotent. Delivery failures report pending control
rather than falling back to the legacy host queue. Pending abort commands are
eligible for background retry only while their original turn, owner and accepted
binding remain current. Pending steering sends use the same background retry
rules. A rejected steering outcome leaves its source parked for explicit release.
Interrupted recovery replays steering results from the old journal; after full
recovery, missing results become `steering_outcome_unknown` failures without
automatically replaying their input. Bounded abort escalation remains unfinished.

The runtime projection marks unresolved source rows with `steering_pending` and
advances its revision as intent/outcome changes. The UI shows delivery confirmation
pending and disables source cancellation/redispatch until it settles. SQL guards
enforce the same restriction independently of the client. Guest delivery means
the driver's control input accepted the instruction, not that its external work
has completed.

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
The state response also carries the driver's protocol version, driver ID and
input schema. The host validates them against the recorded execution before
delivering replay. This allows readiness retries after the original `hello` was
acknowledged and collected, without treating attachment itself as ready state.

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

Historical journal recovery uses a recorded `recover_journal` operation for a
retired execution, carried by a separate process in a later maintenance epoch.
The origin retains its original backend, session, generation, turn and receipt
watermark. Recovery commits output and receipts atomically and feeds the existing
durable projector, including for an already-failed turn. Recovered completion
checks its final output watermark but does not change turn status, advance native
state or publish a successful completion. Old claims, resumes, heartbeats and
start acknowledgments have no execution side effects.

The operation pins the journal head and remains unresolved after an interrupted
read or ingest. Retries use the same reader key and durable origin watermark.
Completion requires all records through the captured head and a scoped process
end event. A rejected signal to an already-exited reader is not termination
evidence by itself. This recovery service does not select the outcome of an
interrupted turn or automatically start a replacement agent.

## Engine event recovery

Engine events are retained in a separate inbox scoped by durable engine store
identity. Event ID and sequence deduplication, payload-digest checks, reconciliation
requests and cursor progress commit together. A contradictory replay rolls back
the whole page. Historical events never overwrite an environment's accepted
placement epoch or mutation revision; they schedule current inspection instead.
An identical inbox replay advances a rewound cursor without repeating the
invalidation, so readiness obtained after the original event remains valid. A
new event still invalidates readiness, and contradictory replay still fails.

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

## Opaque execution bindings

The store can reserve a generation against an observed retained environment
without creating legacy network profiles, runtime-resource proofs or source-IP
contexts. The binding pins backend store identity, sandbox, environment fence,
placement epoch, driver/configuration manifests and the observed process handle.
Acceptance requires that the environment still matches, its operation lane is
settled and no engine event has requested reconciliation.

The bound bridge store requires current acceptance to claim or resume turns and
authorize gateway requests. Pending engine reconciliation blocks those actions,
including replayed claims and requests. The original accepted process can still
settle its already granted turn: start receipts, output, steering outcomes and
completion retain their turn identities, replay checks, output watermarks and
native-state validation. Settlement does not clear the reconciliation request or
restore dispatch authority.

Ingress still requires the current product owner, unexpired leases, the exact
backend/sandbox/epoch/environment fence, an active session and a settled lifecycle
operation lane. A replaced or retired execution must use historical recovery.
Gateway authority is separate; this path does not create a model source-IP
context. The legacy lifecycle remains available for sessions pinned to that
backend during migration.

Retained claim and resume requests persist their decisions in
`execution_turn_requests`, keyed by generation and request ID. Both grants and
no-work decisions commit atomically with any turn lease and service-grant changes.
Retrying an empty or busy claim cannot consume a prompt queued later. A successful
retry returns the original grant payload, including its expiration and replay
flag, without renewing the lease or changing the response command identity.
Resume requests require a request ID and pin their original turn. Reusing an ID
for another operation or turn fails. A cached grant still requires current
execution authority and the original live turn attempt; terminal or expired
grants cannot regain authority through replay.

During attachment, claim/resume envelopes are persisted in
`deferred_turn_requests` before their journal receipts are acknowledged. No reply
is sent until manifest negotiation, recovery through the captured journal head,
and binding acceptance have completed. The launcher then delivers pending
requests in bounded pages through the ordinary decision path. It persists each
response command ID before sending; only the matching guest command ACK marks
delivery complete. A lost send or attachment retries the same response even if
the guest has already collected its original request record. Terminal grants are
marked obsolete and never redispatched. Receipt processing retains the original
process/epoch/owner fences and does not restore readiness after an engine event.

## Egress grant and request ledger

Compiled service bindings are stored as dormant exact destinations under a backend
store identity. They activate only for an accepted execution. Turn claim creates
scoped service grants in the same transaction as the lease; completion, failure,
retirement and explicit revocation invalidate them. Revoked routes remain as tuple
tombstones and cannot be reactivated by registration replay.

Authorization joins the trusted original destination to its service, binding,
accepted execution, current turn and unexpired grant. It enforces typed model,
Doris database and OBS bucket/prefix/operation restrictions. Shell cannot gain
model access through a registered route. The request ledger captures immutable
turn/epoch/grant attribution when work starts; a late finish remains attached to
that original turn after another turn begins. Repeated identical finish calls
are idempotent, while conflicting reuse of request identity is refused.

This establishes application authorization and audit persistence. A production
gateway must still derive the destination from the accepted socket, enforce its
protocol-specific upstream rules and close affected established flows on revoke.
Database revocation alone is not a claim that an external stream has terminated.

Session workspace and bridge-journal volumes have independent opaque storage
bindings. Provisioning verifies the pinned backend identity and uses a recorded
idempotency key through the environment operation lane. Lost responses retry the
same request. If the operation committed but its session binding did not, replay
records the already-created volume without another remote create. Existing
bindings are never provisioned again or silently reassigned, including after an
engine store rebuild. Pi's native home is intentionally absent from this volume
map because it belongs to the retained sandbox overlay.

The execution worker records its process-start intent and generation binding
before invoking the agent launcher. `internal/agentbridge` composes the scoped
process carrier with the bound bridge store and pauses at the protocol readiness
barrier. The worker settles the start operation, records the process observation
and commits binding acceptance before opening dispatch. Failed readiness retains
an uncertain start for reconciliation; detaching the carrier does not imply the
process stopped. A second worker cannot reserve a start over an active binding.
Before launch the worker captures the pending engine invalidation sequence.
Successful process readiness clears only invalidations through that sequence in
the acceptance transaction. A later event still blocks promotion, and a failed
epoch/fence/lease check rolls back the invalidation removal with acceptance.

`internal/launchplan` freezes plans from a pinned exported template manifest.
It checks the engine revision/module, bridge version, retained root layout,
required driver capabilities and package identity. Plans require positive resource
caps, a template identity and immutable configuration/asset mounts at the supported
destinations. Guest environment inputs are limited to the Pi model selector and
credential-free gateway URL. The specification digest is computed from the full
plan; callers cannot supply a substitute. Returned plans are independent copies.
This loader supplies release validation for composition; it does not configure or
authorize gateway service routes by itself.

`internal/retainedapp.Attach` composes the validated plan, scoped bridge store,
controller, native validator/restorer, shared workspace writer, inventory scanner
and reconciliation workers for a server. Backend, gateway revocation ports,
product owner, prepared-content root and service/capacity bounds are explicit
inputs. Its background runtime runs inventory and reconciliation together and
cancels both on shutdown or a fatal worker exit. The server exposes application
projection callbacks instead of constructing engine clients itself.

When new-session policy selects Khora, create-mode availability comes from the
validated plan's enabled drivers, without legacy image/provider validation. Legacy
stale-session repair, host-liveness lease renewal and cold-fallback selection
exclude sessions pinned to Khora. This prevents absent host runtime rows from
being treated as evidence of a dead retained sandbox. The normal executable's
deployment configuration and external gateway composition are not yet switched
to this composition package.

The Khora integration profile includes a real worker turn through this path:
keyed volume/environment provisioning, process journal handshake, accepted binding,
claim/start/output/completion, replayable message projection and durable receipt
watermark. Its assertions require no legacy runtime, network or source-IP context
rows. The shared HTTP/scheduler entry path dispatches through an injected
controller for sessions pinned to Khora. The default executable startup still
constructs the legacy runtime until deployment composition is complete.

Session creation persists its backend in the same transaction as the session.
Existing sessions keep their backend when the new-session policy changes.
Migration classifies retained environment bindings as Khora and other existing
sessions as legacy. Both allocators refuse a session pinned to the other backend;
missing or unavailable Khora controllers do not trigger embedded-runtime fallback.
New Khora sessions have opaque workspace references, no fabricated host home and
no memory-checkpoint policy. Scheduled work uses the same dispatch choice, and
scheduled idle release routes to retained pause for Khora sessions.

The execution controller serializes each session's provision/start and idle
release paths. Retries preserve the original generation, remote operation keys,
process handle and desired configuration. A cached connection must still have a
live stream and durable acceptance under the current product owner. Shutdown
cancels blocked provisioning and readiness before closing connections.

Idle release refuses pending work, workspace mutations and unprojected output.
It reserves an immutable pause intent and revokes route authority atomically,
then waits for the configured gateway revoker to acknowledge flow termination.
Verified pause retires the execution while preserving the sandbox, overlay and
volume bindings. Lost replies retain the original transition precondition.
Resuming a completed idle release creates a fresh generation in the next epoch
using the same sandbox and volumes; Pi selects its saved native session before
the next turn. Configuration changes and unexpected placement replacement
require separate reconciliation. Restart adoption of a live execution is not
implicit in this controller path.

Idle executions have an explicit same-epoch attachment path. The current product
owner fences dispatch, revokes and drains old grants, and inspects the recorded
sandbox/epoch/revision before connecting to the original process handle. It uses
`ConnectProcess`, never a replacement start. Fresh manifest negotiation and
durable processing through the captured journal head precede acceptance. Turn
requests are deferred during replay; terminal output is accepted only if it
matches already-committed producer records. An in-flight turn or unprojected
output blocks this path.

A durable adoption marker preserves interrupted attachment attempts. Retries
cannot fall through ordinary process startup, and an event arriving after the
captured invalidation sequence prevents acceptance. The background reconciler
selects idle bindings from a replaced owner, expired idle leases, and unfinished
adoptions. Successful adoption keeps the generation, process handle, epoch and
native history; it does not reactivate old turn grants.

Active turns have a separate adoption marker that pins the original turn and
attempt. The current product owner transfers the execution and turn leases,
fences dispatch, and revokes/drains old grants before inspecting and attaching
the original process. Startup retries preserve the marker and grant revision;
ordinary Ensure cannot fall through to a replacement start. Both leases renew
together while replay is in progress. Original output, steering results and
native completion can settle during this fenced phase. Terminal duplicates must
match committed records; unknown late output requires historical recovery.

After the recovered journal head, manifest evidence, leases and event watermark
are accepted, the binding becomes active or idle according to the recovered turn
state. A still-running turn receives new grant identities; revoked grants remain
revoked. An existing abort intent prevents fresh grants. Pending control commands
retain their original issuer in storage and use the current accepted owner for
delivery, without changing command IDs or payloads. Deferred request replies use
the original decision ledger, including a grant whose first send was partial.
The turn attempt, process handle, placement epoch and native history are retained.

The background adoption loops select owner changes, expired leases, unfinished
markers and engine-event invalidations. They can revalidate an active same-epoch
process once an event has been ingested. The event source is not yet continuously
polled by the retained application runtime. Unexpected placement or backend
identity changes remain fenced; inspection alone cannot accept them.

After exact placement inspection, a missing process or an attachment precondition
failure enters interrupted recovery through the background reconciler for either
idle or active adoption. A process exit observed before the replay barrier has the same classification. The attached
process stream is also checked for up to one second when the initial
synchronization write returns a transport error; EOF or timeout alone does not
prove process exit. The durable adoption marker permits this transition from `starting`; ordinary
startup cannot use it. Recovery reserves the conditional pause before
remote I/O, drains routes, replays the retained journal, restores completed native
history and ends paused. Its durable operation is picked up by the interrupted
recovery loop after a restart. No replacement agent starts during recovery.
Transport outages, cancellation, incompatible protocols and changed backend
identity do not trigger this fallback. A changed placement still fails the
conditional lifecycle fence.

Explicit idle recovery can fence an abandoned execution under the current product
owner even when its previous lease expired. It preserves queued prompts and
reuses the conditional pause/resume path to restore native history in a new
epoch. A leased/running turn, unfinished projection or workspace mutation blocks
this path. Recovery does not silently replay input or adopt authority from the
previous owner.

Interrupted execution recovery has a durable ledger spanning gateway drain,
verified pause, maintenance resume, historical journal replay, message/trace
projection and final pause. Each step reserves its immutable operation before
remote I/O. An unresolved workflow blocks unrelated lifecycle work and workspace
mutations, including between completed steps. Current product ownership is
checked at every transition, so a replacement owner can continue after a crash.

After verified writer termination the old execution is retired, but unfinished
turn classification waits for recovered output and projection. A producer that
died without completion gets a host failure boundary after its complete retained
head is ingested, allowing partial text to be projected. At the final verified
pause, still-leased or running turns become `execution_interrupted` failures;
already-terminal turns retain their outcomes and queued prompts remain queued.
The workflow does not redispatch input or resume an agent. For Pi, it restores
the selected native file from the last completed snapshot after historical
projection and before final pause. The recorded state version, selected path,
native session ID, size and digest are pinned in a `restore_native` operation.
Missing or changed snapshot evidence keeps recovery unresolved. The helper
preserves interrupted native bytes separately, then atomically replaces and
synchronizes the selected file. An uninitialized state remains uninitialized.
Completed recovery can subsequently resume through ordinary controller readiness;
the original interrupted turn is never automatically replayed.

Retained Pi executions validate native state through the guest helper, using the
accepted backend/sandbox/epoch reference and `/root/.pi/agent/sessions`. The helper
checks the native JSONL session header ID and performs the file/parent durability
barrier before returning a digest. Store completion checks the execution's pinned
specification digest instead of a legacy host contract, and successful Pi turns
must carry native state advancement. The completion lease is rechecked at the
current time after validation finishes.

Completed Pi validation also writes a separate snapshot beneath
`/root/.pi/agent/sessions/.harness-completed`, named by the canonical state digest.
It copies the quiesced native bytes into a separate inode, verifies the native
session ID, and synchronizes the file and parent before returning evidence.
Existing snapshot names cannot be replaced with different bytes. Guest root can
still modify retained files; later readers must verify the recorded digest.

The state-advancement transaction records a `native_capture_requests` row with
the original generation, epoch, turn, selected path, snapshot path, size and
native digest. Completion cannot commit without that row and snapshot evidence.
Retries retain one request per state version. The request is independent of
memory/evolve, and preserving the source does not wait for host archival.

`cmd/native-capture` consumes those requests independently. Each batch selects
the oldest pending state per session, reads its retained snapshot through the
confined guest source, and verifies recorded size, digest and native session ID
before publishing the content-addressed archive and existing trajectory rows.
A durable receipt follows successful publication; a crash before that receipt
retries the same bytes and archive identity. Skipped test sessions get explicit
receipts. Failures in one session leave its request pending while other sessions
can progress.

The worker holds a canonical per-control-database publisher lock, opens only
verified managed schemas, and serializes publication to preserve state ordering.
Its source accepts a later epoch of the same retained sandbox but retains the
request's producing epoch. Changed backend identity is rejected. Paused sources
create a durable activation request and remain pending. Legacy host-path capture enumeration excludes retained Khora
sessions so it cannot overwrite their native projections.

The controller's capture-maintenance service reserves capacity and resumes the
same paused environment without an agent, turn claim or service grant. The
capture process retains read-only responsibility. Maintenance ends after receipts
cover its captured backlog, its fixed window expires, or new user work arrives;
the controller then verifies pause before releasing the lane. Competing lifecycle
changes and workspace writes are blocked throughout the window. Lost resume/pause
replies retain conditional intents, and the current product owner can reconcile
them after restart. Definitively rejected activation is followed by verified
pause before the workflow is released. Application startup must poll the pending
activation requests; the injectable execution reconciler performs this polling.
Default executable composition still needs to select that reconciler.

The retained execution reconciler runs independent sequential loops for pending
aborts, steering sends, workspace mutations, capture activations, idle and active
adoption, and interrupted-recovery ledgers.
Each loop uses bounded pages, per-action deadlines and a rotating key cursor so
old failures do not hide newer obligations. A task that ignores cancellation
continues to occupy its loop; it is never restarted merely because a deadline
expired. Shutdown waits for the loops to stop. Current-owner checks precede each
page and actions retain their own identity/fence checks.

HTTP uploads and background mutation recovery share one writer instance with a
cancelable per-session lock. Recovery cannot stop a helper while its upload
request is still using it. Waiting recovery can time out without touching the
helper, and unused lock entries are removed. The durable mutation fence remains
the cross-restart admission mechanism. The server maintenance entry point starts
the reconciler when one is supplied during composition.

Runtime snapshots include opaque execution bindings. Accepted bindings appear
live without legacy resource rows;
starting bindings appear starting. Epoch/fence mismatch or pending engine
reconciliation cannot appear live. Binding acceptance and invalidation advance the
same session revision used by HTTP/SSE convergence.

Active-session admission counts environment capacity independently of generation
readiness. Known running or unknown retained placements, including those whose
generation or session failed, remain counted. Pending or uncertain create/resume
intents also count before any sandbox reply is recorded. Only verified pause or
a definitive rejected activation releases that active slot. This count is a
product concurrency limit; Khora's configured memory reservation remains a
separate admission commitment even while paused.

Historical engine events invalidate capacity observations. A current inspection
may settle that uncertainty only under the same environment fence, epoch and
mutation revision, with no later event than the captured watermark. A running
inspection does not clear agent-readiness invalidation. Unexpected epochs remain
unknown and counted for recovery. Upgrades initialize existing environment
observations to unknown rather than inferring termination from old generations.

Workspace downloads and share snapshots select their reader from the session's
persisted backend. A missing Khora reader returns unavailable rather than falling
back to a host path. Live reads use the confined guest helper. Paused reads use
bounded volume ranges without activating compute, under a durable read lease.
The lease prevents lifecycle activation, worker reservation and workspace writes
while the adapter rejects symlinks at every path component. This supplements
Khora's volume-root confinement, which permits links within the volume.

Range reads preserve seeking and HTTP download behavior; short volume replies
fail rather than masquerading as complete files. Closing a download releases its
lease. Startup releases abandoned leases only after acquiring the product process
lock and recording the replacement owner. Reader leases have no timer that could
permit a writer to race a slow live download. The API reader adapter is injectable;
default startup still awaits full Khora composition.

Live workspace uploads and editor writes can use the injected retained writer.
It streams bounded input into a durable, content-addressed preparation store,
then reserves the workspace fence and records content size/digest and the exact
guest placement before issuing a helper. Preparation is verified again before
transmission. Turn admission and environment mutations cannot cross that fence.

A lost reply leaves the write uncertain. Recovery under the current product
owner observes the original helper's termination, removes only staging entries
for that operation, then verifies and synchronizes the destination. Matching
bytes settle success; a definitive mismatch settles failure without overwriting
later content. Unavailable termination or durability evidence keeps the fence.
An issued write never retransmits its input automatically. Prepared content is
retained for recovery; reclamation is not yet wired.

For an existing paused workspace, the write service atomically reserves active
capacity and a conditional maintenance-resume intent with the content mutation.
It starts no agent and grants no service access. The helper uses the newly
observed epoch. File success or failure is recorded while the mutation fence
stays held; a conditional pause must settle before the final result releases
turn admission. Lost resume, helper or pause replies preserve their original
identities. A replaced product owner can continue the same workflow. Preparation
failure before helper issuance also completes a verified pause. Ordinary session
resume uses the last verified upload-maintenance pause.

An upload before the first turn can invoke the controller's workspace provisioner.
It verifies the session backend and current owner, pins the normal launch plan,
creates the workspace/journal volumes and retained sandbox with stable keys, then
verifies pause without allocating an execution or launching an agent. Sandbox
creation and initial resume reserve the configured active-session capacity before
remote dispatch. The ordinary write workflow then uses that paused environment.
The first turn resumes the same sandbox and volumes through normal readiness,
preserving files uploaded before agent startup. Repeated provisioning verifies the
existing desired plan and reuses its identities.

Retained artifact inventory uses bounded full scans through the engine. Live
directories are read through the confined helper in pages of 32; paused scans
hold an offline read lease and use bounded volume listings without compute.
Symlinks, special files and incomplete upload staging are excluded. The walk
limits entries, path length and depth, and never publishes a partial result after
an error or overflow.

The control store atomically applies changed files, removes absent files and
records their durable `artifact.updated`/`artifact.deleted` events. A scan must
still match its recorded identity, current owner, environment fence/epoch/revision,
engine-event watermark and workspace-mutation sequence. A newer scan supersedes
an older one; intervening lifecycle or upload work rejects the old result.
Concurrent guest writes can require a subsequent scan, so the scanner also offers
a bounded periodic backstop. Completion, uploads and artifact API refreshes call
the injected scanner. Default startup still needs to run that periodic service.
Legacy host watchers exclude sessions pinned to Khora.
