# Interaction architecture implementation

This ledger distinguishes implemented behavior from remaining refactor work.
Synthetic fixtures do not establish production performance. Provider continuity
is qualified only for the explicitly recorded real-provider run below.

| Stage | Status | Scope |
| --- | --- | --- |
| P01a | Verified | Truthful initial conversation loading; bounded opt-in request timings |
| P01b | Verified locally | Authenticated read sharing, bounded scheduling and cached conversation return |
| P01c | Verified with reset fallback | Scoped file invalidation, truthful watch health and immutable preview ownership |
| P01e | Pending | Comparative latency, idle and resource qualification |
| P01d | Verified locally | Lazy process mounting, stable Markdown file inputs and indexed message lookup |
| P02a | Verified locally | Atomic known-schema migration and durable display storage |
| P02b–d | Verified locally | Durable projector, bounded view APIs and controlled scale comparison |
| P02e | Verified locally | Browser cutover; raw snapshot reconstruction and routes removed |
| P03a/b | Verified locally | Resource scopes and bounded shared server replay hub |
| P03c–e | Verified locally | Incremental browser views, shared transport, idle windows and disposable runtime qualification |
| P04a protocol | Verified locally | Reusable confined guest reader and multiplexed host client |
| P04b–e | Verified locally | Reusable host readers, immutable captures, scoped directory cursors and read-path removal |
| P05 host writer/retention | Verified locally | Bounded priority admission, derived retention and raw-outbox removal |
| P05 journal/flow | Verified locally | Guest reserve, bounded delivery, safe command compaction and delayed-ACK qualification |
| P05 native settlement | Verified locally | Original prompt identity through retry/compaction and capture after complete settlement |
| P05 combined resource audit | Pending | Final cross-phase queue/resource matrix and deployment qualification |
| P06 admission | Verified locally | Bounded format validation in a Worker and preview parse/resource admission |
| P06 Office ownership | Verified locally | Prepared replacements, cancellation, chart lifecycle and localized keyboard controls |
| P06 interaction qualification | Partial | Office cycles and constrained presentation checks pass; history and comparative latency matrix remain open |
| P07 admission fence | Verified locally | Persisted drain ownership/deadline and atomic new-work fencing |
| P07 readiness | Verified locally | Atomic blocker/lease check and revocation by new evidence or cancellation |
| P07 activation | Verified locally | Digest-bound quiescent handoff and explicit compatible rollback |
| P07 owner handoff | Verified locally | Same-contract active native transfer between distinct executable artifacts |
| P07 gateway lifetime | Verified locally | Independent process, bounded authority and native fixture continuity across owner outage |
| P07 real-provider handoff | Verified for recorded run | One GLM-5.2 stream retained across a distinct owner executable update |
| P07 owner/gateway rollback | Verified for recorded pair | Retained revisions preserve new native history, held intent and workspace bytes |
| P07 deployment qualification | Pending | Broader engine/template/schema matrix, bootstrap and combined release qualification |

## Diagnostics

Product responses carry a server-generated `X-Request-ID`; it confers no authority
and is independent of mutation identity. `producthttp.Config.ObserveHTTP` opts into
bounded category/status/byte/duration observations and a `Server-Timing` header
for time to response headers. Stream duration includes the stream lifetime.
The observer must be concurrency-safe and must not block serving. Application
configuration `http_metrics: true` enables structured category-only timing logs.

Browser diagnostics are disabled by default. Build with
`NEXT_PUBLIC_INTERACTION_METRICS=1` or use `configureInteractionMetrics(true)` to
enable a 512-sample memory ring; disabling clears it. Categories are conversation,
directory, file, control and index. Network is fetch-to-headers; parse includes
body transfer and JSON decoding; commit measures reducer/state scheduling, not
React paint. Queue timings are reserved for the resource scheduler. No paths,
actors, content, credentials or request identifiers are retained in these metrics.

## Qualification

P01a adds deterministic browser cases for a three-second nonempty load, successful
empty content, pending and failed/denied responses. Transport tests exercise
stream flush, response deadlines and WebSocket hijacking with timing on and off.
Validation: frontend lint, typecheck, 124 unit tests, production build and five
loading browser tests passed. Product HTTP race tests and vet passed.
Remaining phase acceptance, real runtime benchmarks and release qualification
are pending; no deployment has been performed as part of this refactor.

## Shared read lifecycle

An authenticated application owns one memory-only resource store. Unmount and
reauthentication cancel its reads and clear content. Keys distinguish project,
resource kind, identity/page and optional placement epoch. Exact-key consumers
share a request; only the last cancellation aborts it. Dirty generations coalesce
finite invalidation bursts. Commands and terminal streams bypass this store.

The scheduler permits four concurrent reads, at most two file/directory reads per
project, and 128 waiting requests. Saturation returns a visible capacity error.
The memory cache estimates decoded overhead, caps entries at 256 and content at
32 MiB, evicts inactive entries, and refuses oversized entries. Drafts and pending
command identities remain independently owned. Session return restores its cached
reading window before validation; directory refresh preserves visible rows and
serializes distinct continuation demands.

Validation: 128 frontend unit tests, lint/typecheck, production build, six loading
and cache browser cases, and the existing navigation/draft/edit/terminal case
passed. A delayed-load browser test caught invalidations starving in-flight reads.
Page demands now serialize with content catch-up and retain independent anchors.
Fifty-cycle browser heap/resource qualification remains pending; server
subscription lifecycle tests already cover fifty attach/detach cycles.

## Conversation rendering

Completed turns mount process content only while expanded or when a message
navigation target requires it. Final answer copy reads the complete message model.
Workspace link arrays retain identity when their paths do not change, and message
lookup uses one indexed map per window. Equal timeline rows retain object identity
across refreshes. Browser find and print cover mounted content; expand a completed
turn or use turn navigation to inspect its process history.

Validation: the completed-turn production browser case verifies zero collapsed
process containers, expansion, target navigation, final-answer copy, elapsed time
and feedback. Frontend lint/typecheck and 128 unit tests passed. Full CPU/heap
profiles and comparative performance targets remain unqualified.

## File events and preview ownership

File events retain workspace-relative current/old paths, operation, entry type,
placement epoch, sequence and reset reason. Epochs/sequences serialize as decimal
strings. Invalid paths produce a reset rather than exposing an unconfined path.
The browser validates only affected directories and selected files; unknown scope
validates at most sixteen visible directory candidates, with a 64-path dirty set.

Watch reconnects use the shared resource hub replay. Expired or lost continuity
requires explicit reset validation; connection events never imply external edits. Reconnection indicators begin after
five seconds; fifteen seconds exposes delayed state. Heartbeats carry no content
invalidation. The adapter acquires quiet subscriptions asynchronously, with `Next`
owning the wait for the first frame; cancellation also covers pending attachment.
The shared replay hub preserves a live upstream watch across planned browser
rotations, including sequence-zero empty workspaces.

Preview validation hashes actual returned bytes. Identical bytes retain the same
representation; changed versions of the same file retain readable prior content
while downloading and preparing. Each Office renderer owns an isolated browser
document with a transferred input buffer. The old document stays visible until
the replacement reports ready; removing a document ends its URL, timer and global
handler scope. Late callbacks cannot mutate the replacement. Denied/missing file reads clear retained preview pixels. Unsaved
editor text is separately owned and compared against external bytes without being
overwritten by watch events or delayed saves.

Validation: all 47 production-build browser tests passed, including two simulated
rotations, 100 unrelated writes producing zero selected-body reads, same-size
external edits, pending-save edit protection, keyboard navigation, and both
locales/themes across desktop/narrow viewports. The 130 unit tests and Go HTTP/Khora
race tests passed. The disposable Khora fault suite passed native runner, files,
PTY, lifecycle, recovery and service authority cases, including two watch resumes.
The first runtime run exposed a quiet resumed-watch attachment deadlock; its fixed
rerun passed. These checks do not establish final performance SLOs or Office
format/resource limits.

## Display storage migration

The exact original schema upgrades to version 2 under the exclusive owner lock.
All schema steps and version changes share one transaction; unknown schemas are
rejected without repair. Display tables hold immutable source identities, derived
items, versioned details, changes, turn summaries and checkpoints. A source-index
trigger records ingestion identity without moving ACK responsibility into the
projector. Existing raw data, messages and command state remain authoritative.

Validation includes interruption before commit, baseline data preservation,
backup restoration to a separate database, post-upgrade writes surviving reopen,
foreign schema rejection and owner-lock alias checks. Product and HTTP race suites
and vet passed. The browser now uses the derived view API. Old binaries are not
a supported downgrade; current schema steps include projector work queues and
actual outbox retention frontiers.

## Durable display projector and queries

Schema 3 persists incomplete native message groups and indexed pending-session
work. A fair worker processes at most eight sessions per pass, one bounded batch
each. A batch reads at most 128 records and approximately 1 MiB; a larger legal
record progresses alone. It stops after 512 accumulated changes. Native messages
with more than 512 display blocks fail projection explicitly without losing raw
records or changing ACK/control outcomes. Unknown event types are retained and
skipped; malformed known display fields fail without advancing the checkpoint.

Items keep their first appearance positions within authoritative turn order.
Native identity includes incarnation, turn, start sequence and block. Tool updates
retain the original call position. Imported messages remain readable. Items,
tombstones, versioned details, change records and checkpoint commit atomically.
Projection status is independent of execution completion. Idle workers do not
rewrite checkpoints. Retirement removes derived private data along with source
state after the existing cleanup gates pass.

The view API reads indexed derived rows, with 50 items per page, at most 100 delta
operations, a 64 KiB inline item limit and a 256 KiB encoded response limit. Larger
items expose exact full JSON through bounded UTF-8 detail chunks. Delta pages
freeze their target revision and retain an ordinal inside partially delivered
revisions. Old generations or expired changes require resync. Rebuild replaces
only derived state and never dispatches commands. The browser uses these pages and changes. Snapshot/raw-record HTTP routes and
request-time raw timeline reconstruction have been removed.

A deterministic local comparison held output at two items across 1,000 and 10,000
raw records, with one warmup and twenty timed samples. Snapshot query p95 grew from
5.48 ms to 52.11 ms; derived-page p95 was 0.29 ms and 0.36 ms. These values qualify
this fixture only. Large cumulative payloads, writer contention and combined
workloads remain to be measured.

Validation includes semantic shadow comparison, reused native runners, missing
message starts, out-of-order tools, process close/reopen, injected transactional
failure, replay, imported messages, tombstones, exact large-detail reconstruction,
fixed-target deltas while new writes commit, bidirectional pages, scoped cursors,
authorization, fair sibling progress and idle-write absence. Go product/HTTP race
suites and the disposable Khora fault suite passed. A full-suite PTY test failure
exposed stale resize-frame synchronization in the test; the corrected case passed
ten consecutive race runs. Schema-compatible deployment rollback qualification remains open; browser
cutover is implemented and tested.

## Shared resource event hub

`GET /api/projects/{project}/resources/events` multiplexes content, control,
summary, terminal, lifecycle, authorization and file hints. Each active actor/
Project scope shares one product-outbox poller and one advisory upstream watch.
The application owns hub shutdown before closing the Store. Last-reference release
keeps a thirty-second rotation grace period, then cancels the watch; subscriptions
never create lifecycle activity or activate a paused Project.

An opaque replay cursor binds protocol, Project, hub incarnation and sequence.
The ring holds at most 4 MiB / 4,096 events / ninety seconds. Each subscriber holds
at most 256 envelopes / 1 MiB; overflow requests resync and disconnects that reader.
Each hub admits at most 64 subscribers and an application at most 128 hubs.
Write deadlines are five seconds. Reauthorization remains every 250 ms, heartbeats
arrive every five seconds and planned rotation after twenty-five seconds. Rotation
preserves the hub and watcher. Hub restart, replay expiry, upstream gaps and
placement changes explicitly invalidate continuity. Product/file/transport cursors
remain separate domains. Native raw events do not send redundant content hints;
authoritative command records publish control hints independently of display work.

Race tests verify empty-scope rotations, replay, expiration, incarnation/resource
fencing, overflow isolation, HTTP reauthorization, fifty attach/detach cycles and
cleanup without Project activation. The separate Project/file streams and old
conversation reducers are removed. Runtime hub qualification is recorded below.


## Incremental browser cutover

The browser repository tracks each loaded page's content frontier and catches it
up before merge. Fixed-target delta continuations preserve partial revisions;
tombstones remove rows. Off-window changes do not accumulate history. At most
sixteen pages / an estimated 8 MiB remain active; edge eviction preserves a
continuous reading window and its anchor. A growing live tail reacquires a bounded
page instead of retaining every item. Generation/retention resync targets the
current reading item. Pending target loads retain their selector through retries.

Content hints refresh only the matching conversation. Sidebar child/control hints
refresh the named child; terminal and file scopes stay independent. Normal
rotations perform only head validation. Hidden tabs release their transport and
revalidate visible resources on return. Disconnected tabs use a thirty-second
light metadata fallback. Raw snapshots, raw-record reducers, separate file-watch
streams and the old multi-Agent preference migration have been removed.

The backend supplies bounded turn-summary queries, recovery/control metadata,
exact-item anchors and immutable finalized-message chunks. Copy retrieves complete
message text independent of mounted blocks. Missing navigation targets remain
separate from lost Project authority. Revocation clears authorized memory and
preview pixels. Schema 4 records actual pruned outbox IDs, so global ID gaps caused
by other Projects do not manufacture replay loss.

The full 49-case browser suite passed, followed by focused checks for added
scope/hidden-tab/pending-target cases. The 60/120-second post-ready fixture windows
measured zero heavy reads and zero new preview representations; rotations issued
two/four head requests respectively. These are real elapsed windows using a
controlled transport fixture, not field INP or real-network idle measurements.
The released real-application browser gate passed both workflows, including
concurrent native model fixture requests, Stop, PTY output, Files/Office previews,
editing, sharing and Chinese navigation. Agent completion under PTY output was
1.07 seconds in that run.

A five-minute disposable mixed run completed sixty rounds with four agents,
two PTYs, file operations, schedules and owner restart. It recorded 1,297 HTTP
samples (p95 4.05 ms, p99 10.01 ms), 249 admission-to-projection samples (p95
473.95 ms), and peak owner RSS 37,244 KiB. The earlier baseline used a different
request mix: these are workload observations, not an isolated causal speedup.
Final combined overload, document-budget, release-continuity and rollback gates
remain P04–P07 work.


The standalone real hub gate passed in 52.48 seconds: the initially empty workspace
reported a zero source sequence, two browser rotations produced no extra file
invalidations, one upstream watcher remained, and the connected observer allowed
pause and watcher cancellation. The original recovery suite passed independently;
the new fifty-second timing scenario is a separate test so it cannot change that
suite's idle-agent assumptions. Runtime service authority also passed. Shared
readers now receive content-change events only when finalized-message sequence
advances, with metadata-only reauthorization on reconnect failures.

All existing Go tests/build/vet and relevant race suites passed. Source comparison
checks confirm that the former snapshot/raw-record query and independent owner
SSE/watch routes no longer exist; route-removal tests return 404. Later sections
record fifty-cycle browser heap tests, constrained document budgets, saturation
fixtures and a real-provider owner handoff. The final combined resource/journey
matrix remains outstanding.


## Reusable reader protocol qualification

The workspace helper has a versioned read-only service mode using framed process
stdin/stdout. The guest reuses its confined root descriptor, multiplexes at most
two read/stat/list operations and cancels requests independently. Frames are
bounded before decode; content chunks are 32 KiB, request frames 16 KiB and reply
frames 256 KiB. Idle readers exit after thirty seconds. Requests have a two-minute
operation deadline. No mutation operation is accepted by this protocol.

Directory continuations use immutable sorted metadata observations: up to 100,000
names, 32 MiB estimated metadata per snapshot, sixteen snapshots and 64 MiB retained
metadata per reader. Expired/evicted cursors explicitly resync; initial lists
create a new observation. Nanosecond timestamps are hints only. Read completion
carries exact captured size/SHA-256; observable file changes during transfer
prevent success. Strong HTTP semantics await the host capture stage.

The guest and host-client race tests passed. Disposable runsc tests passed the
100,000/100,001 boundary, concurrent symlink substitution, frozen pagination under
mutation, cancellation, fifty requests through one process, and existing runtime
fault/recovery/service gates. In one disposable run the cold reader handshake took
11.15 ms; fifty warm stat requests took 208.59 ms total. These are prototype
measurements; HTTP still uses the old file-operation path until the next cutover.


## Confined file read cutover

File HTTP reads and frozen-share capture now use the reusable reader exclusively.
`Files.Execute` and the one-shot guest command dispatcher accept mutations only;
the read/list command path, per-read mutation row and stdout transfer interface
are removed. Schema 5 persists one helper binding per Project for orphan cleanup.
A replacement helper first retires the original handle; uncertain cleanup never
creates duplicate processes. List/watch do not activate Projects. Explicit content
open retains activation intent. Placement/authority checks cancel read leases on
pause/delete/epoch change without treating them as permanent activity blockers.

Reader protocol 2 introduces per-request chunk credits. Eight chunks may be in
flight; four credits return after consumption. Cancellation waits for its terminal
acknowledgement before reusing that slot. An unconfirmed cancellation or partial
input-frame send invalidates the connection. Large transfer throttling is confined
to its request instead of rejecting ordinary disk delays. At most sixteen host
read leases and readers, two per Project and one list per Project, are admitted.
Leases time out after five minutes; guest operations after two minutes. Reader
idle exit is thirty seconds and host idle retirement starts after twenty-five.

Content is captured into unnamed `O_TMPFILE` inodes under the application's
`read-captures` state directory, avoiding accidental use of a memory-backed system
temporary directory. Captures are capped at 256 MiB each /512 MiB aggregate. They
are hashed and reopened read-only before publication. No named files survive
owner death, and release decrements byte/FD/lease ownership. Each stalled HTTP
body write is bounded to thirty seconds. The operator status includes reader,
lease and captured-byte counts. Conditional requests reacquire current bytes;
Range uses only one complete immutable representation. ETags never imply save
compare-and-swap or an atomic historical filesystem observation.

Runtime manifest version 3 retains reader protocol 2 and adds native settlement
protocol 1; older runtime manifests
are rejected. New immutable images were assembled and qualified in disposable
substrates; no existing user template/service was replaced. In the final reader
fixture fifty warm captures reused one helper and performed zero database writes;
its cold capture took 25.21 ms and fifty warm captures 379.85 ms total. Encoded
page tests include filenames whose JSON expansion exceeds raw filename size.
The full runtime gate passed release, a five-minute sixty-round mixed workload,
browser workflows, realtime rotations, files/PTYs, recovery and service authority.
The mixed run recorded HTTP p95 4.81 ms/p99 8.59 ms, projection p95 337.62 ms and
peak owner RSS 37,612 KiB. These are local observations; saturation calibration,
final cross-phase resource plateaus and deployment continuity remain open.

The final immutable reader image also passed its released executable and real
browser gates after moving capture storage to unnamed state-directory inodes.
The browser cases took 29.8 seconds; Agent completion under PTY output was 903 ms.


## Host writer admission and derived retention

SQLite transactions enter an explicit bounded admission gate. Stop has thirty-two
reserved queue slots, independent of thirty-two ordinary control slots. Ingestion
has at most thirty-two queued requests and 16 MiB of queued payload/overhead.
One background batch may wait; additional background attempts return pending and
retry without marking a projection failed. Four control admissions permit an
ingestion opportunity, alternating Projects while preserving FIFO within each.
Background work also receives bounded scheduling opportunities under sustained
foreground pressure. The running transaction remains atomic and cannot be
preempted. Operator status reports bounded-category queue peaks, wait/transaction
time, admissions, rejections and cancellation; control byte reservations are
fixed one-MiB estimates, while ingestion counts its retained payload bytes.

Native record ingestion now updates only durable evidence, authoritative messages
and its watermark. It no longer increments metadata revisions or emits a redundant
raw-token outbox event. Display commits publish content frontiers; command outcomes
publish control changes. ACK still follows committed ingestion only.

Schema 6 accounts for immutable change bytes, including referenced large details,
and records actual pruned revision/ordinal frontiers. Each session's replay high
watermarks are 16 MiB /8,192 operations; pruning targets half those limits in batches
of at most 256 operations. Crossing a watermark pauses further projection batches
until bounded cleanup catches up. The just-committed input batch is the bounded
overshoot. Current item details and versions referenced by retained changes remain
readable. Explicit garbage candidates avoid rescanning all current detail payloads.
Raw evidence, command identities and native captures are not pruned by this policy.
Expired delta bases require resync; continuations beyond a pruned ordinal remain
valid within their fixed target. Rebuild retains the same source identities.

Outbox cleanup uses per-Project indexed seeks and actual count/frontier accounting,
with hot scopes selected first and at most 1,024 deletions per Project batch. Old
read-command rows migrate to process-only cleanup identities; mutation identities
and uncertain write outcomes remain. The obsolete read-history retention loop is
removed. Reader cleanup rotates across Project/handle tuples so many orphaned
handles in one Project cannot hide later entries.

Tests cover full ordinary-control and ingestion queues, independent Stop capacity,
FIFO/Project fairness, bounded background progress, cancellation, atomic pruning,
partial-revision delta continuation, pinned details, source rebuild, and migration
of unresolved read helpers while preserving unknown writes. Legal cumulative
40/80 MiB host-ingestion fixtures keep both Projects progressing. The final recorded
80 MiB run completed in 3.35 seconds, with Stop admission 35.86/5.86 ms and 27.50 MiB
of combined replay data across two sessions. These are local host-stage observations;
guest journal pressure, actual native Stop delivery and encoding qualification
are still open. Existing Go checks/race tests and the disposable runtime fault
suite passed; no service deployment was performed.


The final real browser run passed with 942 ms Agent completion under PTY output.
An earlier concurrent-verification run recorded 7.5 seconds and a lifecycle
startup deadline; the isolated retry restored 890 ms but exposed a menu that
failed to dismiss after Office moved focus. Capture-phase Escape handling and its
regression test fixed that defect, then both real browser workflows passed.
These observations are retained separately from host-only pressure measurements.


## Guest journal reserve and delivery

The default 64 MiB logical journal budget reserves 16 MiB for abort/state-query
receipts, command results and native response/message-end/tool-end/agent-end
evidence. Ordinary updates and new prompt/steer receipts use the other 48 MiB.
The reserve is one quarter of configured capacity, capped at 16 MiB; it does not
raise total capacity or promise unlimited terminal output. Journal logical bytes
are distinct from SQLite/WAL physical bytes and the Project runtime-volume quota.

A pressure-blocked native append holds one legal frame and releases the command
mutex while waiting for ACK. After five seconds without sufficient space, the
runner records an unknown outcome where possible and fails closed; no sequence is
assigned to unwritten data. Command dispatch remains write-ahead. On ACK, at most
thirty-two resolved command payloads are compacted; their IDs, digests, outcomes
and results remain. Received/dispatching/unknown and active prompt payloads remain
intact. A partial index keeps compaction independent of accumulated resolved
history. Known journal format 1 upgrades transactionally to format 2.

Delivery reserves at most 1 MiB /64 unacknowledged records; a larger legal frame
can progress alone under the existing 4 MiB frame limit. Control replies are
selected between records, carry no duplicate result body, and have 32-message /
64 KiB reservation bounds. Initial sync re-ACKs the host's durable prefix, fixing
an idle reconnect after loss of the final ACK. Host ACKs still depend only on
committed ingestion. Pressure telemetry cannot advance a committed watermark.

Agent wire version 2 adds bounded one-second pressure telemetry. Operator status
reports advisory live `agent_pressure` samples, including logical/physical journal
occupancy, high water, full attempts, unacknowledged record count, delivery-window
and control-queue reservations. No content or credentials are included; absent or
zero initial samples are not execution evidence. Runtime images require the new
agent protocol. SQLite connections now configure FULL durability directly; the
unused NORMAL writer factory and override-by-string-replacement are removed.

A controlled 100 ms ACK-delay fixture delivered every assigned sequence and
matched native payload/order hashes for 40/80 MiB cumulative traffic. The 80 MiB
run completed in 14.8 seconds, peaked around 50.2 MB logical journal usage and
1.05 MB send reservations, waited for space 45 times, and ended with zero
unacknowledged records. Native stdin abort writes in that fixture took less than
0.3 ms. A real Pi/runsc case with ACK deliberately withheld filled its normal
journal, dispatched Abort in 10.34 ms, then resumed ACK after syncing the fixture
transcript and settled with genuine native canceled evidence. Its journal stayed
at 1,509,513 /2,097,152 bytes with a 524,288-byte reserve.

The full disposable runtime suite passed release, sixty-round mixed work,
browser, watch continuity, Files/PTYs, recovery and service authority. Mixed HTTP
p95 was 5.83 ms and projection p95 441.59 ms; peak owner RSS was 38,252 KiB. These
are local workload observations, not field performance guarantees.

Raw payload compression/delta encoding is deferred: the measured uncompressed
fixtures remain within the configured budget and preserve control progress with
bounded backpressure. Revisit if representative legal-frame workloads repeatedly
hit the five-second stall boundary despite healthy ACK/ingestion, or measured
sustained payload rate exceeds the qualified consumer rate. Increasing journal
capacity alone is not the fallback; raw source and unknown outcomes stay intact.


A bounded encoding comparison used 189 legal numeric/text snapshots: 84.07 MB raw
became 18.34 MB with gzip best-speed, with 584 ms encode and 398 ms decode time and
exact decoded byte equality. This prototype is test-only. Compression is a viable
future option, but it is not selected merely from compression ratio; representative
transport/persistence bottlenecks remain the trigger above. Final immutable runtime
qualification passed after direct FULL verification and indexed command compaction.

## Preview admission and validation

Rich previews now have admission limits before the Office libraries or image
decoders receive their bytes. PNG/JPEG and OOXML workbooks, documents and slides
are supported within these bounds. Legacy XLS, PDF, SVG, animated/other image
formats, embedded fonts/media and unsupported binary parts offer download; they
have no qualified bounded decoder in this implementation. Text/Markdown remains
limited to 1 MiB. Downloads retain the complete original file.

| Resource | Admission limit |
| --- | --- |
| Downloaded preview | 16 MiB |
| Retained preview reservations per tab | 128 MiB, including overlapping representations |
| Estimated Office renderer | 64 MiB |
| ZIP entries / expanded bytes | 1,024 /16 MiB total, 4 MiB per entry |
| XML elements / nesting | 100,000 /64 levels |
| Workbook | 8 sheets, 20,000 cells, 2,000 rows, 256 columns; sparse grid extent also bounded to 20,000 cells |
| Presentation | 40 slides |
| Document | 2,000 paragraphs, 100 explicit page/section boundaries |
| Image | 8 million pixels each, 16 million across an Office package |
| Parse admission | One active operation, two queued operations |
| Retained Office renderers | Two documents: displayed and preparing |

ZIP validation checks central/local names, bounded byte spans, actual decompressed
length and CRC, XML well-formedness, structural counts, and raster dimensions.
DTD/entities, external embedded resources, nested arbitrary archives and ambiguous
names fail validation. One level of embedded XLSX receives the same validation
and contributes to aggregate reservations; printer metadata is bounded but ignored.
The reservation formula estimates expanded models, elements, cells and decoded
pixels. It is not a browser heap limit, and the separate 32 MiB resource cache,
application code and browser overhead remain outside it.

Validation runs in a dedicated Worker using native bounded decompression and
`saxes` 6.0.0 (ISC; `xmlchars` 2.2.0, MIT). Its input buffer is transferred, not
cloned between contexts. Cancellation and a ten-second deadline terminate the
Worker. Office rendering still executes on the main thread; asynchronous library
APIs do not imply a Worker. Renderer reservations last through document removal; the parent does not pass
its blob URLs into renderer documents. Canceled queued work never starts. Same-byte
revalidation skips validation and renderer replacement. Diagnostic builds expose
`batteryPreviewUsage()` with aggregate reservation, URL, Worker, observer,
renderer and queue counts; it contains no file names or contents.

Controlled production-build profiles used 200/20,000-cell XLSX,
10/1,000-paragraph DOCX and 2/40-slide PPTX fixtures. Initial main-thread validation
added observable blocking; transferring validation to a Worker restored the large
spreadsheet's observed long tasks to 83–96 ms in the comparison run (173–215 ms
with main-thread validation). These are single diagnostic observations, not p95
or field INP. Existing DOM-backed renderers are retained. The profile harness
records initial visible shell and adapter completion separately; older shell
observations alone do not prove complete spreadsheet rendering.

Regressions cover dishonest ZIP sizes/checksums, malformed/deep XML, sparse grid
bombs, external embedded content, entry limits, image dimensions, cancellation,
queue/reservation exhaustion and visible download fallback. Generate synthetic
Office files with [the document generator](../frontend/e2e/preview-documents.py)
and run [the opt-in profile](../frontend/e2e/ui/preview-profile.spec.ts) with
`PREVIEW_FIXTURES` and `PREVIEW_PROFILE_OUTPUT` pointing to separate input and new
output directories. Use `NEXT_PUBLIC_INTERACTION_METRICS=1` for resource counters.
The ownership and lifecycle qualification below follows this admission slice.
The complete history, interaction and comparative latency matrix remains open.

## Repeated preview lifecycle

A desktop Chromium journey warms all three Office formats, both conversations
and the account menu, then repeats fifty file/session/fullscreen/theme cycles.
Each checkpoint returns to the same Markdown file, allows 500 ms for the viewer's
200 ms debounced layout callback and file validation to settle, and performs GC.
The [lifecycle test](../frontend/e2e/ui/preview-lifecycle.spec.ts) checks owned
resources, total live object URLs, DOM nodes, listeners, retained heap, keyboard
focus and the unsent conversation draft. Optional `PREVIEW_HEAP_SNAPSHOTS=1`
collects heap snapshots for attribution.

This exposed a workbook debug log in the pinned spreadsheet library. Browser
DevTools retained every logged workbook, including theme XML. The frontend now
transpiles that dependency through the console-removal compiler transform;
workbook and clipboard debug logs are removed, while warnings/errors remain.
The test also rejects recurrence of the workbook log. Heap retainer analysis
confirmed zero additional theme strings from these console roots after the fix.

In the qualified desktop run, all six post-GC checkpoints retained 1,091 DOM nodes,
517–519 listeners, one Markdown URL and 147 bytes of owned preview reservations.
There were zero retained Office renderers, preview observers, Workers, active
parses or queued parses at these checkpoints. Heap growth stayed within the
predeclared 20% /2 MiB allowance; most earlier growth was compiled-code warmup.
The final ten cycles added approximately 93 KiB, with no resource-count growth.
These observations qualify this fixture and stable point, not every Office
feature or constrained device. The following ownership stage extends this
qualification to prepared replacements and repeated chart-bearing documents.

## Prepared Office rendering

Office renderers now run in separate same-origin browser documents. This isolates
spreadsheet media globals and the presentation library's chart callback bus.
One admitted parse can prepare a candidate while the displayed document stays
interactive. A successful candidate restores document scroll and slide position and takes over;
cancellation, failure or a fifteen-second preparation deadline leaves the prior
version available with retry/download. Same-byte revalidation creates no new
document. Pending documents are hidden from keyboard and accessibility navigation.
The parent accepts readiness only from the candidate's window and origin.

Buffers are transferred into these documents; renderer-created URLs belong to
that document and are revoked when its browsing context is removed. The old
shared-document renderer and global listener interception are removed. This is
lifecycle isolation, not off-thread DOM rendering or a separate security origin.
Two renderer/document reservations remain the maximum. Diagnostic counters now
include live documents as `frames`.

Presentation controls use native buttons with English/Chinese labels and a live
slide counter. They restore slide position on replacement and forward Escape to
the workspace. Viewer theme follows application theme changes, and resizing
scales the slide to its available width. The pinned presentation source also
needed a scoped lifecycle fix: its no-argument unsubscribe retained callbacks,
and deferred chart initialization could target an already removed slide.
The build/dev hook verifies the exact upstream source digest, removes those
callback roots on slide changes/destruction, and skips detached chart targets. Missing explicit fills now use document theme
colors, absent chart titles stay absent, and preview chart animation is disabled.
Slide viewport height follows the actual aspect ratio, avoiding cropped 4:3
slides. Paint checks require nontransparent bars rather than only an SVG shell.
No library version or license changed (pptx-preview 1.0.7, ISC); an upstream
source change fails the hook until reviewed. A regression executes the actual
patched event methods through one hundred slide retirements.

Six equal-fixture production CPU profiles passed before/after document isolation.
Single-run adapter completion added approximately 30–140 ms, with no observed
long task above 100 ms in that comparison. Fifty ordinary and fifty chart-bearing
file/session/fullscreen/theme cycles passed, including repeated chart slide
navigation. In the chart run the final checkpoint retained one application
document, one Markdown URL, 147 reservation bytes, and zero Office documents,
renderers, Workers or queued/running parses. Post-GC heap increased from 10.66 MB
to 12.29 MB; the final ten cycles added 47.8 kB. The earlier shared-document
comparison ended near 20.87 MB. These are local fixture observations, not field
memory or latency guarantees.

Replacement tests hold the new document's response while interacting with the
old one, then check takeover, chart preservation, canceled/expired preparation,
explicit retry, same-byte reuse and a late canceled request after text selection.
Presentation keyboard/theme/resize checks pass in English and Chinese, light and
dark, desktop and narrow viewports, with reduced motion and four-times CPU
throttling on narrow cases. Remaining P06 work includes history/Markdown profiles,
complete-answer/anchor/accessibility qualification and repeated latency trials.

## Conversation layout and reading intent

The conversation now keeps a visible block as its layout bookmark and observes
the scroll viewport plus one content container. Font changes, viewport resizing,
composer growth and process expansion can alter height without a view revision;
they no longer leave an opted-in follower above the bottom or displace a paused
reader. Browser scroll anchoring is disabled for this container so it does not
compete with the application's page and block compensation. Automatic layout
scrolls cannot overwrite the pre-layout bookmark or silently change follow mode.
Pointer, wheel, touch and paging-key input supersede navigation suppression.
Opening process details pauses following; Return to latest resumes it.

A 1,000-turn synthetic fixture exercises bounded surrounding windows, older/newer
paging, real font-size changes, viewport/composer resize, appended streaming turns
and complete-answer copying beyond the displayed excerpt. A separate controlled
profile loads sixty turns with collapsed process history: approximately 32,834
DOM nodes, with no process containers mounted. Thirty profiled keydown-to-next-
animation-frame samples under four-times CPU throttling had a nearest-rank p95
of 34.3 ms and maximum 35.7 ms, with no observed long tasks in that typing window.
This measures a lab feedback signal, not field INP, end-to-end page latency or
thirty independent page-load journeys. Much sampled CPU belongs to Playwright
DOM inspection; it is not attributed to the application parser. These results
do not justify adding turn virtualization for this fixture. Large single-message,
initial-render and full comparative journey qualification remain separate gates.

Browser find can discover only loaded pages and mounted process content. Print
is also limited to rendered content and can clip scroll containers. Neither can
discover an unloaded turn or unmounted folded history. Use the turn rail
or a message deep link to load the desired window, expand its process history,
and then use browser find/print. Copy answer fetches complete model text even
when its visible excerpt is truncated. Copy and message-text downloads currently
share the 16 MiB decoded working limit; the workbench does not imply unlimited
full-conversation printing or export.

## Reading bookmarks outside the page cache

Reading position now uses small versioned metadata under the actor/Project/Session
preference key. A paused bookmark contains a stable display-item ID, visible block
index and offset, plus the selected process item's open state and detail scroll.
Following latest stores only that intent. No message text or page bodies enter
the bookmark. Values are bounded and validated; the old absolute-scroll and
last-clicked-turn restoration path is removed.

An explicit message URL takes priority. Otherwise a missing memory window loads
through the existing `around_item` API, reveals the bookmarked process item if
needed, and aligns its available block. Pending projection retains the same
navigation demand. A removed item falls back once with a visible notice; denied
access clears displayed content without replacing the saved bookmark. Navigation
fetching and alignment have separate lifetimes, preventing a missing target from
reissuing the same request on every page update. Draft ownership is unchanged.

The regressions cover warm Session return, page reload with an empty cache,
complete draft retention, nested process scroll, pending/missing/denied anchors
and explicit-link priority. The full default UI suite passes 63 cases; 22 Office
and profile cases remain opt-in and retain their separate qualification evidence.

## Collapsed activity bodies

Active turns now mount a thinking/tool output body only while its row is open
and its activity group is visible. Closed rows retain their summaries, native
open state and scroll metadata. Every pending/running tool summary remains
visible when the group is collapsed; hiding its body does not remove execution
status or cancel a command. Reopening a group restores its previously opened
row and detail scroll. Reading-bookmark restoration follows the same path.

This closes a gap left by turn-level lazy mounting: an unfinished turn previously
parsed thinking Markdown and formatted tool output even inside closed details.
A controlled four-times-CPU profile of forty thinking/tool pairs measured one
524 ms long task, 80 output bodies and 22,198 DOM nodes. With lazy detail bodies,
the equal-fixture run had no observed long tasks, zero output bodies and 5,918
DOM nodes. Time from the synthetic update to the last active summary fell from
750 ms to 119 ms. These are single profiled laboratory runs, not p95 or field INP.
The script retains raw CPU profiles and DOM counts under a caller-selected
`ACTIVITY_PROFILE_OUTPUT` directory; `ACTIVITY_CPU_RATE=4` selects the constrained
profile. No additional virtualization is inferred from this comparison.

Truncated thinking and tool details now offer the existing full-content download
inside the open body. The request uses the immutable detail version, not a
message ID shared with adjacent native blocks. Opening the excerpt does not
fetch full content; only the download action reads its bounded chunks. Regression
checks verify exact downloaded text, running-call visibility, updated open output,
closed-body release and restored detail scroll. The shared decoded download
budget remains 16 MiB; this is not an arbitrary-size output guarantee.

## Bounded Markdown rendering

Rich Markdown now passes a linear pre-parser check: at most 8 KiB of UTF-8,
1,024 lines, 2,048 markup characters, 1,024 table separators, 32 consecutive
delimiters and 16 quote markers on a line. These are conservative admission
limits, not a complete Markdown grammar. Larger or unusually dense input renders
as escaped, selectable plain text with a localized notice. No source text is
silently removed by this decision. File text previews retain their 1 MiB bound;
conversation excerpts and complete-answer/download limits remain separately
defined. Links, tables and code formatting are literal in the plain-text mode.

The selected fallback avoids constructing a large syntax tree merely to discover
its size. The previous four-times-CPU table-heavy file profile measured a 978 ms
main-thread task at 64 KiB and an 11.3-second task at 256 KiB. A 16 KiB rich input
varied from 190 to 298 ms in repeated diagnostic runs, motivating the smaller
8 KiB rich ceiling. In the final comparison, the 8 KiB rich file's largest task
was 128 ms; 16/64/256 KiB plain-text files had no observed long tasks. The 256 KiB
preview completed in 446 ms versus about 13 seconds before. The 1 MiB boundary
still required a 275 ms text-layout task and approximately 1.6 seconds to complete
under this constrained profile. This is a reported limitation, not a universal
sub-200-ms guarantee.

The [Markdown harness](../frontend/e2e/ui/markdown-budget.spec.ts) separates CPU
profiles (`MARKDOWN_PROFILE_OUTPUT`) from independent latency trials
(`MARKDOWN_LATENCY_OUTPUT`). Each latency sample uses a new browser context,
waits until the application is ready, and measures selection to complete visible
text. It excludes application startup and uses loopback fixture responses with
four-times CPU throttling; it does not measure field INP or shaped-network/server
latency. Raw samples include failures and nearest-rank quantiles. Full-source
selection, literal markup handling and dirty-edit retention are checked at the
file-preview boundary; original-file download bytes are checked through the real
Go edge rather than inferred from a mocked browser download response.

The independent loopback run completed 30 trials per size with zero failures.
Nearest-rank selection-to-complete-text p95 was 463 ms for the rich 8 KiB fixture,
247 ms for plain 64 KiB and 1.57 seconds for plain 1 MiB (maximum 2.22 seconds).
Each size had one excluded warmup; browser contexts and application resource
caches were fresh per trial. CPU throttling was four-times, production assets
were prebuilt, and no CPU profiler ran during these latency trials. These figures
qualify those synthetic files and that environment only.

## Navigation and full transcript windows

Turn-navigation metadata now has a 300-entry window, with at most one additional
marker for the current transcript turn. Prompt/final-answer previews are bounded
to 600/1200 UTF-16 units in the browser. Earlier/later paging evicts the opposite
edge and retains its sequence continuation; revision refresh reloads the retained
window rather than merging a disconnected latest tail. One cancellable request
runs at a time, and a pending revision waits for that request instead of being
lost. Marker keyboard navigation retains a tab stop even when the current turn
is outside the index. A separated current marker preserves a route back to the
reader's visible content without pinning an unbounded index.

The owner-only turn-index API now supplies both directional continuations and
accepts one nonzero `before` or `after` selector. Pages stay chronological; a
refresh may request up to 300 entries. Schema 7 adds a partial
`messages(session_id,turn_id,sequence DESC)` index for assistant messages. This
bounds final-preview lookup by the selected turn rather than searching unrelated
Session messages. Query-plan regression requires that index and rejects a scan
or temporary sort. The known-schema migration from version 6 is atomic, preserves
accepted turns, and rolls back cleanly when interrupted before commit. Rollback
must use a build that accepts schema 7; restoring a backup over later accepted
writes is not a supported rollback.

The 1,000-turn traversal fixture loaded twenty-one ten-turn pages. At sixteen
pages it retained 160 turns and 83,420 DOM nodes; after five further page loads
those counts remained unchanged. Post-GC heap was 46.93 MB at sixteen pages and
47.66 MB at twenty-one. Forward paging after eviction and Return to latest both
worked; latest replaced the window with ten turns. This demonstrates a resource
plateau for that fixture, not a constrained-device scroll-latency guarantee.
The separate navigation test traverses beyond its index limit, returns through
later pages, follows revision changes and jumps into an evicted transcript.

## Durable release admission

Schema 8 persists one admission generation, coordinator owner, phase and deadline.
SQLite BEFORE triggers fence new executable turns, held-turn release, prompt/
steer creation and dispatch, Agent/Terminal starts, file mutations, Project
create/resume operations and new content-publication reservations. Gate closure
and these reservations use the same serialized writer. A side effect reserved
before closure remains observable and may settle; queue-only intent cannot start
new native work. Explicit held prompts and metadata creation remain available.

The operator drain/status/abort surface uses the existing mode-0600 control
socket and application writer. Stale owner/generation aborts fail. The persisted
fence survives reopening; a one-second worker aborts expired draining attempts
without killing existing work. Readiness and quiescent activation are qualified below. Durable status counts do not substitute
for runtime quiescence, and this implementation does not claim active-stream
update continuity. See [operations](operations.md#admission-drain).

Regression cases cover concurrent admission, idempotent request replay, preexisting
queued/pending commands, scheduler occurrences that remain unconsumed, held-turn
release, steering, Stop during pending/unknown work, new Agent/Terminal/file/Project
reservations, content publication, deadline/stale-generation handling, persisted
restart state, protected control routes and public HTTP rejection. Migration from
schemas 6 and 7 is checked for pre-commit interruption and retained authoritative
turns. An executable fixture closes admission, restarts the owner, confirms the
same generation remains closed, aborts it and completes new native turns.

The runtime leases below account for reader, terminal-input and title-generation
work. Model forwarding uses durable gateway slots described below. Readiness and
quiescent activation are locally qualified; bootstrap and final rollback
qualification remain. No deployment is inferred
from these disposable fixture results.

## In-flight release work

Runtime lease admission now shares a mutex with persisted release transitions.
The phase read and slot reservation occur under that mutex; drain closure holds
it through the SQLite commit. Remote calls never hold the mutex. Ordinary input
and reads gain no durable per-operation ledger writes. Fixed categories bound
the owner: 8 title calls, 64 terminal inputs, 32 resizes, 32 signal/EOF/termination
operations, 16 workspace reads, 16 reader starts and 64 maintenance operations.
Model requests use at most 256 durable active slots across gateways. Existing
per-resource and per-gateway limits still apply independently.

Title/input work requires open admission. Ctrl-C, signals, resize, reads and model
requests for already admitted turns may continue during draining; the later
ready/activating phases reject new leases. Counts remain positive through backend
return and settlement, even if a caller has canceled. Workspace captures transfer
lease ownership to the captured body and release it on Close. Title leases span
both the model request and conditional title write, preserving user-rename rules.
Gateway authorization records a durable attributed slot and releases it only when
forwarding returns; owner-local model counters and embedded serving are removed.

Tests hold title/input backends across closure, reject new work, preserve Ctrl-C
and termination, verify canceled work stays counted, check idempotent release and
capacity, and confirm read bodies own their leases. Gateway tests observe two
concurrent leases and wait for actual handler settlement before requiring zero.
Runtime status and durable blocker counts are observations, not one atomic
readiness snapshot. The transition below performs that check under the shared
fence. Quiescent activation is described below. Runtime leases require schema 8
or later; independent gateway request accounting requires schema 11.

## Revocable release readiness

Schema 9 centralizes durable release predicates in `release_blockers`, including
Project transitions, guest captures and pending/uncleaned share publications.
Partial indexes contain unsettled candidates, and the readiness view explicitly
uses them so ordinary completed history is not scanned under the writer. New
share publication is fenced while admission is closed. Unknown evidence remains
a blocker unless its turn was explicitly acknowledged and its original process
exited with cleanup settled and no pending replay; acknowledgment never rewrites
the recorded outcome.

`drain-ready` holds the runtime-admission mutex and immediate SQLite writer while
checking owner/generation, deadline, live leases and durable blockers. It commits
ready only when no blocker remains. Expiry aborts the release and reopens admission
without killing work. Ready survives owner restart, retains the same deadline,
and can be explicitly aborted. The handoff stage below consumes ready before activation.

Readiness is revocable. Cleanup, new unresolved evidence, or pending publication
returns ready to draining in the same transaction that records that work. This
revocation rolls back if the work transaction rolls back. Authorized terminal
cancellation revokes ready before acquiring its signal lease; unauthorized
requests cannot do so. Reader-helper startup now owns a separate 16-slot lease
through its handshake, preventing a canceled first reader from concealing an
in-flight startup. Idle reusable readers do not permanently block readiness.

Tests race readiness against reader admission, persist/reopen a ready generation,
exercise expiry/stale generations, introduce cleanup and unknown evidence,
verify revocation rollback and acknowledgment, and hold reader startup after its
consumer cancels. Query-plan checks require the unsettled indexes. The executable
fixture first lets normal idle work settle, marks ready, restarts the real owner,
confirms the same ready generation, aborts it, then completes new native turns.
This qualifies a quiescent checkpoint and restart; it does not qualify active
model-stream handoff or deployment bootstrap. Quiescent activation follows below.

## Quiescent activation handoff

Schema 10 adds the current Store instance, activating source instance, target and
finished executable digests, plus retained coordinator identities. Activation
requires a current ready generation and another atomic work check. The protected
control command persists activating, finishes its HTTP response, and retires the
source application. Activating never expires into open admission. Completion and
rollback require a distinct replacement instance and empty work checks; the
source cannot reopen its own gate. Completed retries must match the recorded
finished digest, and stale coordinator IDs cannot create another attempt.

The replacement hashes its running executable inode and refuses a mismatched
activation target before opening a database writer or migrating schema, as well
as before publication/reconciliation. Explicit rollback-candidate
startup may use a different compatible executable but stays fenced until rollback
is acknowledged. New content publication is limited to the replacement while
activating; resume for pending original-journal recovery has a narrow exception.
All other new execution remains closed. A 64-slot maintenance category covers
lifecycle effects, recovery, capture export, share/file cleanup and Agent stopping,
so the source cannot begin those effects after activation while the replacement
can settle outstanding evidence. The procedure preserves the original database;
there is no restore-over-writes rollback path.

Unit/race tests cover source-instance refusal, stale generation, digest mismatch,
retained accepted intent, late blockers, replacement maintenance/publication,
explicit rollback, coordinator reuse and complete control-response delivery.
The executable fixture activates and observes the source exit itself, starts a
replacement, completes handoff, and executes new native turns. A separate fixture
refuses an unexpected startup, permits an explicit rollback candidate, and proves
the previously accepted held turn survives unchanged. These tests qualify the
handoff mechanism on compatible schema-10 code; they do not certify an older
schema-incompatible executable as a rollback build or real-provider active-stream
continuity. First-deployment bootstrap remains open.


Schema 11 and the protected gateway authority protocol establish durable request
accounting for process separation. Reusable slots fence delayed acquire/release
messages, enforce a global 256-request bound, and retain release blockers across
owner restart. Existing-stream continuation is bounded to five seconds since a
successful check began; confirmed denial cancels immediately and unavailable
checks cannot renew authority. Focused tests cover temporary outage recovery,
expiry, stalled checks, revocation, lost replies, bounded cleanup, registration
replacement and schema-10 migration. Actual provider continuity remains a
separate qualification gate.


## Independent gateway process

The `gateway` command serves declared audiences in a process separate from the
owner. The exclusive lifetime lock precedes registration and is held through all
forwarding handlers. Embedded gateway listeners and process-local model leases
are removed. Only the protected authority RPC can authorize forwarding; request
slots survive owner exit and schema-compatible reopening. The owner does not load
gateway upstream keys. Operator status reports active handlers, pending settlement
and authority unavailable/expiry/denial counters without content or credentials.

A gated `gateway-stop` asks the owner for a fresh atomic ready checkpoint using
the supplied release identity, then requires empty local forwarding and pending
acknowledgments. It finishes its control response before shutdown. A replacement
registers only under the same lifetime lock; no stream is claimed resumable across
that replacement. Forced termination can interrupt streams.

A disposable native fixture keeps a single model request open through an owner
outage and verifies the same gateway PID, original turn/incarnation identity,
exactly one upstream request and genuine native completion. It also refuses a
busy gateway stop, replaces the gateway after drain, and completes a subsequent
native turn. The model is deterministic and the owner replacement uses the same
build: this does not qualify the active-update coordinator, arbitrary schema
migration, an older rollback binary or a real external provider.


Application startup validates activation identity under the exclusive database
lock through a read-only connection before any schema upgrade. Tests start from
schema 10 and prove that a wrong executable, missing source or invalid target
leaves the database byte-for-byte unchanged, including its schema and source
instance. Expected and explicitly acknowledged rollback candidates can still
upgrade recognized schemas without losing accepted turns. An activating state
from before executable-bound handoff is refused. Real executable activation,
rollback and independent gateway/native outage fixtures pass with this ordering.


`release-identity --config FILE` reports a candidate's measured executable and
compatibility inputs without opening application state or contacting dependencies.
Startup uses the same preparation and published content revisions. Unit tests
verify stable identities, configuration/content invalidation, pinned manifest
validation and omission of raw configuration/credentials. The actual executable
fixture compares candidate identity before startup and while the owner holds its
Store lock against `diagnostics.release_identity`. Matching inputs remain a
prerequisite, not proof of active-update safety or arbitrary rollback support.


## Guarded owner handoff

Schema 12 adds an explicit owner-only release mode while preserving full drain as
the default. The mode pins a candidate executable, exact schema/compatibility and
live gateway registration snapshot. A running turn transfers only with a durably
applied original prompt, running incarnation and active Project. Original model
requests for those turns remain counted and may continue through the stable
gateway. Every other durable blocker and all host operation leases still prevent
ready/activation/completion. Late evidence or authority changes revoke readiness.

Each transition uses a fresh gateway observation, at most two seconds old,
validated against the database and pinned snapshot. Owner-mode startup requires
identical schema/contract before opening any writer; migration is forbidden.
Unmeasured Store access cannot bypass it. Source-instance and target-digest checks
remain, and explicit rollback after gateway replacement additionally requires
full quiescence. Gateway stop refuses owner mode even with zero current requests.

Unit/race tests cover rejected identities without database mutation, retained
requests across replacement, admitted requests during handoff, unsafe/unknown
work, stale/changed/omitted gateway observations, late authority revocation,
activation races, deadline abort, source refusal to finish and explicit quiescent
rollback after gateway loss. A native fixture activates a distinct candidate while
a long upstream request remains active, observes the same gateway PID, preserves
held intent, and verifies exactly one request plus genuine native completion with
unchanged turn/incarnation identity. The candidate artifacts use the same source
with distinct build metadata; this is mechanism qualification, not arbitrary build
or real external-provider compatibility. The real-provider observation below
qualifies one configured provider; the broader version matrix remains open.


## Complete native prompt settlement

A failed native fixture exposed that Pi 0.77.0 emits `agent_end` before its retry
and compaction continuations settle. Completing at that event could release a
turn's gateway authority, dispatch another prompt and freeze an incomplete capture.
The guardian now requires the pinned native RPC protocol-1 `prompt_end` event,
correlated to its applied prompt. Its authoritative final outcome precedes
bounded descendant settlement and immutable capture. Intermediate events retain
the original command/turn identity and cannot release its busy reservation.

The image builder applies an exact-input/exact-output patch to the pinned Pi RPC
module. It reports full Promise settlement and supports cancellation of retry,
compaction and preflight continuation. Manifest format 3 requires this capability;
generation and guest verification check the native RPC bytes as well as the tree.
No legacy `agent_end` completion path remains. Go wire/journal and reader formats
remain version 2; the immutable guest release changes and requires full drain.

Tests verify preserved command identity, no premature capture/descendant cleanup,
rejection of foreign/unapplied settlement, and reserved capacity at normal journal
exhaustion. Native fixtures verify provider retry followed by success, queued
prompt exclusion during backoff, cancellation without another provider attempt, exhaustion before one failed
settlement, post-run compaction in the exported capture, and cancellation during post-run and
preflight compaction. These deterministic failures qualify lifecycle behavior;
they do not replace the real-provider release gate.


## Real-provider handoff observation

A [sanitized qualification record](../dev/measurements/owner-handoff.json) captures
one ModelArts MaaS GLM-5.2 run through the released native runtime and independent
gateway. The provider emitted 425 deltas over 24.313 seconds (113,542 SSE bytes)
and ended normally with `finish_reason: stop`. The owner switched to a distinct
compatible executable in 127 ms while the original stream remained active. The
gateway PID/registration stayed unchanged, with one unavailable authority check
and no denial or expiry. Exactly one provider request and one original native
applied/settled/completed sequence were observed; no prompt was resubmitted.

The isolated substrate used a protected Unix relay to the real provider. The key
remained on the host; only synthetic context was sent. Native retries and tools
were disabled, generation was capped at 1,536 tokens, and reports retained no
prompt/response text or credentials. The relay neither paced nor synthesized the
stream. An earlier 4,096-token trial hit the three-minute relay deadline and is
recorded as failed qualification, not excluded from the account of results.

This establishes the specified active-stream observation for this provider,
configuration and compatible build pair. It is one run, not a latency distribution,
a guarantee for arbitrary providers/builds, or proof of bootstrap/rollback support.
The complete supported-version matrix and combined workload remain separate gates.


## Retained-revision rollback observation

A [recorded rehearsal](../dev/measurements/revision-rollback.json) exercises owner
and gateway revisions `525ec8f → cc8dc88 → 525ec8f` on the same schema-12/native-1
runtime. It accepts writes after the upgrade, then verifies five completed native
turns, unchanged held intent and exact post-upgrade file bytes after rollback and
both gateway replacements. The current client source is identical at both revisions;
its browser/Files/PTY workflows also pass against the retained owner. The
[compatibility matrix](compatibility.md) states the exact pair, rollback window and
remaining cross-version gates. No accepted-write backup restore or legacy reader
was introduced.
