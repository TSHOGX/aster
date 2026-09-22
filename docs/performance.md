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
| P02b–e | Pending | Projector, bounded view APIs, comparisons and cutover |
| P03 | Pending | Shared replayable resource events and incremental views |
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
passed. A delayed-load browser test caught polling incorrectly marking in-flight
reads dirty; polling now shares the read without manufacturing an invalidation.
Live watch performance and fifty-cycle resource qualification remain pending.

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

Watch reconnects currently use explicit reset validation. They do not claim
continuous replay or count as external edits. Reconnection indicators begin after
five seconds; fifteen seconds exposes delayed state. Heartbeats carry no content
invalidation. The adapter acquires quiet subscriptions asynchronously, with `Next`
owning the wait for the first frame; cancellation also covers pending attachment.
A shared replay hub is still P03 work.

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
and vet passed. The new tables are not yet served by HTTP; bounded projector and
consumer cutover are the next stage. Old binaries are not a supported downgrade.
