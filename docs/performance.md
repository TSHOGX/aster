# Interaction architecture implementation

This ledger distinguishes implemented behavior from remaining refactor work.
Measurements from synthetic fixtures do not qualify production performance or
active-model deployment continuity.

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
| P04 | Pending | Reusable confined readers and immutable representations |
| P05 | Pending | Fair byte budgets, control reserve and retention |
| P06 | Pending | Measured browser and document rendering budgets |
| P07 | Pending | Admission/drain, independent gateway and combined qualification |

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
while downloading. Renderer leases hold object URLs through asynchronous cleanup.
Each Office parse owns a separate stage, preventing late callbacks from mutating
its replacement. Denied/missing file reads clear retained preview pixels. Unsaved
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
SSE/watch routes no longer exist; route-removal tests return 404. Full fifty-cycle
browser heap plateaus, constrained document budgets, saturation and a final
real-provider owner-release continuity test remain explicit outstanding work.
