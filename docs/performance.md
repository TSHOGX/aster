# Interaction architecture and qualification

The application bounds work by requested content and owned resource lifetimes.
Conversation display reads use an indexed durable projection; file reads use a
reusable confined reader; browser views retain bounded windows. Native command
outcomes, authority and recovery remain separate from those display mechanisms.
The [final qualification record](../dev/measurements/final-qualification.json)
closes the code and documentation refactor. Measurements below qualify stated
fixtures and build combinations, not universal latency or total-memory guarantees.
No live deployment is implied.

## Authenticated browser reads

One authenticated application owns its resource store and Project transports.
Logout, revocation and scope removal clear affected content and fence late
callbacks. Exact-key consumers share a request; canceling the last consumer aborts
it. Dirty generations coalesce a finite invalidation burst into one successor.
Page and anchor demands remain distinct. The conversation repository separately
limits serialized ordinary reads to eight and reserves one coalesced navigation
worker. Superseded target demands collapse to the latest target; excess ordinary
work reports capacity without consuming that slot. Commands and PTYs bypass these
schedulers.

The scheduler allows four reads overall, two file/directory reads per Project and
128 waiting requests. Aging and Project fairness prevent selected content from
being displaced indefinitely. Capacity failure is visible. The store retains at
most 256 entries and 32 MiB of estimated content, subject to pinned-view admission.
Only assembled conversation windows, directory trees and eligible preview bytes
are retained; per-request replies do not duplicate them. Drafts and command
identities have separate ownership and are never evicted as cached reads.

Initial loading, pending projection, errors and successful empty results have
different UI states. Returning to a cached conversation restores its own window,
draft and reading position while current authority/content are validated.

## Conversation projection and delivery

Raw native records remain authoritative evidence. A fair worker projects at most
eight Sessions per pass, one bounded batch each. Batches read at most 128 records
and approximately 1 MiB; one larger legal record can progress alone. A batch stops
after 512 changes. Native messages exceeding 512 display blocks fail projection
explicitly without altering command completion or ACK authority. Unknown event
types are retained and skipped; malformed known display data cannot advance a
checkpoint as if it had been rendered.

Stable item identity includes native incarnation, turn, start sequence and block.
Items, tombstones, immutable detail versions, changes and frontiers commit
atomically. Rebuild changes derived state only. Idle projection does not rewrite
checkpoints. Retirement removes derived private content after cleanup gates.

Authorized view APIs provide head, seek pages, fixed-target changes, scoped
cursors, turn summaries and full-content chunks. Pages contain at most fifty
items; deltas contain at most one hundred operations. Encoded page/change replies
are capped at 256 KiB, inline items at 64 KiB and detail source chunks at 32 KiB.
Large items expose exact immutable detail versions. Expired generations/replay
require explicit resync; pending projections never masquerade as empty content.

Each browser window retains its own frontier. Newly loaded pages catch up before
merging, and updates preserve stable item identity. The active transcript retains
at most sixteen pages and approximately 8 MiB of decoded state. A delta window
resyncs above two hundred items or approximately 1 MiB. Edge eviction preserves
continuations and reading intent; it does not join disconnected history.

One actor/Project resource hub shares an outbox poller and advisory filesystem
watch. Its replay ring is bounded to 4 MiB/4,096 frames; each of at most sixty-four
subscribers has a 1 MiB/256-frame queue. At most 128 scoped hubs coexist. Slow consumers resync without blocking
fast readers. A thirty-second last-reference grace permits browser rotations.
Authorization, Project identity and opaque cursors remain scoped. Empty workspaces,
zero-sequence watches, restart, overflow and actual gaps have explicit semantics.

The browser shares one Project transport and routes hints to affected resources.
Healthy idle does no heavy conversation/body/directory polling. Head/heartbeat
work is counted separately. Hidden tabs release transports and reconcile on return;
a thirty-second light metadata fallback operates only while disconnected.

## Files and active directory windows

[Paused-volume qualification](../dev/measurements/paused-volumes.json) covers
conversation/file access, storage mutations with no occupied compute, retained
workspace bytes after resume, quota failure and interrupted-operation handling.

For active compute, the guest reader reuses a root descriptor confined to `/workspace`. Component
walking uses `O_PATH|O_NOFOLLOW` and verifies object types; symlink substitutions,
special files and private runtime paths are refused. The reader accepts no writes.
It multiplexes two operations, with 16 KiB request frames, 256 KiB replies and
32 KiB content chunks. Eight chunk credits bound each transfer, replenished four
at consumption. Host per-request receive queues hold at most twelve frames.

Host admission bounds reader bindings/leases to sixteen overall, two reads and
one directory scan per Project. Guest operations expire after two minutes;
unused host/guest readers retire after twenty-five/thirty seconds. Cancellation
has request identity. A failed cancellation invalidates the connection, and
replacement settles the original process handle before creating another.
List/watch and content opens never activate compute. Paused/new Projects use
volume APIs directly, with bounded admission and the existing lifecycle execution
lane protecting I/O from a concurrent resume. Completed content captures release
that lane before delivery. Reads create no per-read mutation or activity rows.

A directory snapshot freezes sorted metadata, at most 100,000 names, 32 MiB each,
64 MiB retained, sixteen snapshots and a thirty-second lifetime. Concurrent changes
do not silently mix snapshot pages. Capacity and expiry are explicit.

The browser independently retains the root plus sixteen open directories, one
hundred entries per directory, at most 512 KiB estimated per page and 8 MiB total.
Next/First controls replace pages. Closing cancels reads and releases descendants;
late replies cannot restore a closed branch. Expiry returns to the first page
with a notice. Deep links read bounded ancestors rather than searching entire
inventories. Selected previews and unsaved edits remain owned outside the current
page. Filename search has separate bounded traversal state: one metadata page per
ancestor, at most 64 levels and 8 MiB estimated retained pages, plus one 100-result
window. A batch issues at most 100 directory reads and yields before another read
after five seconds; further work requires explicit continuation. Search uses the
shared per-Project directory admission, never fetches file bodies, and cancels on
query change, hiding the panel or authority loss. Expired cursors rescan only the
current directory, skipping already processed names.

Each content read captures at most 256 MiB into an unnamed `O_TMPFILE` inode under
application state, within a 512 MiB aggregate capture limit. Size and SHA-256 are
verified before exposing a read-only representation. Strong ETags and single
Range/If-Range responses describe those exact bytes. Authority and bytes are
rechecked before 304; metadata-preserved rewrites cannot falsely appear unchanged.
Unsatisfiable ranges return 416; multiple ranges fall back to full content.
Detectable concurrent mutation returns `changed_during_read`. Truncated transfers
abort the response, and stalled response writes have a thirty-second deadline.
These validators do not provide atomic compare-and-swap editing.

## Conversation layout and reading intent

Completed process content and closed tool/thinking bodies mount only when opened
or explicitly targeted. Their summaries remain accessible; opening a body does
not fetch its full immutable detail automatically. Copy/download retrieves complete
content within a separate 16 MiB decoded working limit.

Reading bookmarks retain stable item/block identity, offsets, process-open state
and follow intent independently of cached bodies. Empty-cache return reloads the
surrounding item. Explicit deep links take priority; a removed bookmark produces
a notice. Font, viewport and composer reflow preserve the chosen anchor. User
scrolling or process inspection leaves automatic following until latest is chosen.
Browser find/print cover mounted content; turn navigation and explicit expansion
provide access to other history.

The turn rail retains three hundred indexed rows plus a current marker, with
bounded prompt/answer excerpts and one cancellable request. Earlier/Later controls
page explicitly; programmatic scroll restoration cannot trigger extra fetches.
A scoped final-message index prevents unrelated Session scans.

## Bounded Markdown rendering

Rich Markdown admits at most 8 KiB UTF-8, 1,024 lines, 2,048 syntax characters,
1,024 pipe characters, thirty-two consecutive delimiters and sixteen quote markers
per line. Larger or denser input uses complete escaped selectable text within the
file/excerpt bound, with a localized notice. Markup and links are literal in that
fallback. File text previews remain limited to 1 MiB; downloads retain the original.

A controlled four-times-CPU comparison reduced a table-heavy 256 KiB preview from
a thirteen-second rich render to bounded plain display. Separate thirty-context
plain-text trials observed p95 247 ms at 64 KiB and 1.57 seconds at 1 MiB. The large
plain-text boundary still incurs layout cost; this is not an arbitrary-document
sub-200-ms guarantee. Full copy, selection, dirty edits and real download bytes
have separate regressions.

## Preview admission and validation

Text/code/Markdown/HTML/JSON/table previews up to 256 KiB can reopen from authenticated
memory while a visible checking notice accompanies a fresh authorized read.
Identical digests preserve the representation; denied/missing files clear it.
Known events invalidate affected cached bytes even for unselected files, and
successful saves replace cached bytes. Drafts remain separate.

Office and image inputs pass bounded validation before rendering. ZIP names,
headers, decompression, XML depth and document structure are checked in a
cancellable Worker. This does not move Office DOM rendering off the main thread.
Unsupported formats or excessive work produce an explicit download fallback.

| Resource | Bound |
| --- | --- |
| Downloaded preview | 16 MiB |
| Estimated retained preview reservations per tab | 128 MiB |
| Estimated Office renderer | 64 MiB |
| ZIP inventory / expansion | 1,024 entries; 16 MiB total; 4 MiB per entry |
| XML | 100,000 nodes; depth 64 |
| Workbook | 8 sheets; 20,000 cells; 2,000 rows; 256 columns; bounded sparse extent |
| Presentation / document | 40 slides; 2,000 paragraphs; 100 pages |
| CSV/TSV | 100 displayed rows; 2,000 parsed rows; 256 columns; 20,000 cells |
| Source/edit text | Valid UTF-8, no NUL bytes, at most 1 MiB |
| Image pixels | 8 million per image; 16 million total |
| Parse / renderer lifetime | One active parse; two queued; two renderer documents |

These are admission estimates, not browser RSS measurements. Each Office renderer
owns an isolated same-origin document and transferred input buffer. The previous
representation stays visible until replacement readiness. Cancel, supersession,
timeout and late callbacks cannot replace the current document. Removing the
owned document ends its timers, URLs and global library handler scope. Localized
keyboard controls, chart paint, aspect ratios, theme and reduced motion are tested.
The pinned presentation fixes are hash-checked; workbook debug logging is excluded
from the production bundle to avoid retaining parsed workbook objects.

## Writer, journal and retention budgets

SQLite admission reserves thirty-two Stop slots independently of thirty-two
ordinary control slots and thirty-two ingestion slots/16 MiB queued ingest bytes.
Only one background waiter is admitted. Four control admissions permit ingestion;
ingestion alternates Projects, preserving FIFO within each. Background work gets
a bounded opportunity after sixteen foreground admissions. Transactions are not
preemptible. Rejections, cancellation, queue peaks and wait/transaction durations
are observable; control byte reservations are estimates.

Live Agent ingestion batches adjacent records from one original incarnation into
at most sixteen records or 256 KiB, with one larger legal record allowed alone.
Collection waits at most five milliseconds. The decode queue independently admits
sixteen records/256 KiB, with one larger legal frame alone and cancellation-aware
byte credits.
Controls and ownership boundaries flush the current batch. Raw records, command
outcomes and watermark commit together; any invalid member rolls back the whole
batch and returns no ACK. FULL durability and original record order remain.
This reduces per-record transaction overhead when consumers accumulate a backlog.

Display replay has high watermarks of 16 MiB/8,192 operations, targets half those
limits and prunes at most 256 rows per step. Indexed outbox pruning removes at most
1,024 rows per Project batch. Current and replay-referenced detail versions remain
pinned. Raw evidence, unresolved command identity and required capture/rebuild
inputs are not deleted to disguise pressure.

The default 64 MiB logical guest journal reserves one quarter, capped at 16 MiB,
for controls and terminal/settlement evidence. Ordinary traffic uses the remainder.
Journal-pressure waits release the command mutex, keeping native Stop available.
A five-second no-progress deadline leaves unknown effects unknown. The send window
reserves 1 MiB/64 unacknowledged records; one larger legal frame can progress alone
under the 4 MiB wire limit. Control responses have separate bounded queues: thirty-two responses within a
64 KiB estimated byte budget and eight sync replies. Payloads remain in the
durable journal rather than being duplicated in the control lane.
ACK follows durable host ingestion, never mere transport delivery. Compaction
retains deduplication and unresolved evidence.

The retained [40/80 MiB pressure record](../dev/measurements/queue-pressure.json)
includes actual writer count/byte peaks and wait times, journal logical/physical
peaks, delayed ACKs, send-window occupancy and control progress. Compression or
checkpoint/delta wire encoding remains deferred: the measured workload progresses
with bounded flow and durable ACK. Reconsider encoding if representative healthy
consumers repeatedly exhaust the five-second progress window. No alternate legacy
decoder is retained for that optional optimization.

Fresh format-2 journals create the current layout and required indexes atomically.
Format-1 conversion and startup index repair are removed; old formats, versioned
empty databases and missing required indexes are refused without rewriting their
evidence. Pi `agent_end` is not completion: the pinned `prompt_end` protocol settles
retries/compaction before descendant cleanup, immutable capture and command outcome.

## Global schedule reads

The schedule overview returns bounded metadata without full prompt bodies. The
browser retains one fifty-row page and refreshes explicitly or after local writes;
there is no periodic overview polling or subscription per Project. Selected-task
reading/editing fetches full scoped detail; pause/resume revalidates the saved
revision. Title/status/Project filtering and chronological ordering happen on
the server before paging, with explicit cursor invalidation on concurrent writes.
Filter/unmount cancellation and selection checks fence late reads and mutations.
Conversation choices and history retain one bounded page, preserving drafts.
Overview loading/errors never imply emptiness.

## Release boundaries

The durable admission coordinator fences user and internal dispatch together.
Unknown commands, cleanup, file mutations and runtime leases prevent quiescent
readiness. Generation/owner checks, revocable readiness and executable-bound
activation prevent stale coordinators from reopening admission. Candidate identity
is checked before the writer opens under the exclusive lifetime lock.

An independent gateway owns upstream streams and credentials. Existing authorized
streams continue at most five seconds since a successful authority check began;
confirmed denial stops immediately and new requests fail closed on owner outage.
Durable bounded request slots retain cleanup responsibility across owner restart.
Explicit compatible owner handoff transfers only eligible applied native work
and pins the live gateway registration. Gateway replacement requires full drain.

Default and retained runtime manifests form a bounded host-validated catalog.
Every non-deleted saved Project must match its exact template ID/digest. Placement
manifests are checked against current host capabilities before guest self-verification.
Catalog changes and upgrades from builds without that contract require full drain.
See [operations](operations.md), [compatibility](compatibility.md),
[gateway authority](gateway-contract.md) and the [bootstrap no-go](bootstrap.md).

## Recorded qualification

| Observation | Evidence and scope |
| --- | --- |
| Startup and initialized journeys | [Retained/current paired comparison](../dev/measurements/startup-comparison.json): equal synthetic content, thirty fresh contexts per build; final current local return p95 25.7 ms versus 53.2 ms; all earlier timing variation retained |
| Indexed query scaling | [Thirty samples at 1k/10k/100k records](../dev/measurements/conversation-scale.json), identical canonical two-item output; current query p95 0.23/0.23/0.40 ms versus retained raw reconstruction 6.81/62.53/792.53 ms; excludes HTTP/rendering |
| Combined browser/native load | [Final bounded ingestion run](../dev/measurements/final-combined.json): 52 rounds, all 217 completions, completion projection p95 378 ms, provider-to-ready p95 1.33 s, HTTP p95 26.15 ms, owner/gateway peak RSS 66,488 KiB, 1.235 s owner update |
| Runtime idle | Same combined record: separate 60/120-second windows, zero heavy reads/new preview URLs and unchanged preview node; head reads counted separately |
| Current guest runtime | [Journal image qualification](../dev/measurements/journal-runtime.json): release, mixed, browser, hub, native pressure, Files, PTYs, recovery and service authority; original pressure timeout and later diagnostic passes retained |
| Real upstream continuity | [One GLM-5.2 stream](../dev/measurements/owner-handoff.json): 24.3 seconds, one request/original native completion, stable gateway and 127 ms distinct owner switch; scoped to that build/provider pair |
| Office latency | [180 fresh-context trials](../dev/measurements/office-latency.json): normal-CPU DOCX/XLSX/PPTX p95 774/577/505 ms; four-times-CPU 813/1,230/1,025 ms; routed fixed bodies and useful DOM/canvas readiness |
| Maximal transcript scrolling | [160-turn, sixteen-page profile](../dev/measurements/history-scroll.json): four-times-CPU wheel-to-RAF p95 8.8 ms, largest observed task 139 ms, 83,421 DOM nodes; automation inspection is not application attribution |
| Directory retention | [Fifty-page resource journey](../dev/measurements/directory-window.json): 100 rows, fixed 6,740 nodes/751 listeners, post-GC heap 11.67→11.96 MB; selected dirty edit preserved |

Fifty chart/session/fullscreen/theme cycles also retain the declared preview
resource plateau. Correctness cases cover delayed/denied responses, content/delta
races, replay, scope revocation, directory/byte confinement, capture validators,
40/80 MiB pressure, native cancellation and durable recovery. See the
[verification runbook](verification.md) for commands and fixture definitions.

These observations do not establish field INP, arbitrary document rendering,
cross-host recovery or conversion of a retired installation. Combined runs retain
long tasks above 200 ms and earlier failed workload attempts; thresholds were not
relaxed to erase them. The startup comparison separates initialized feedback and actual quiet windows.
The resource/source inventory and local qualification are complete for the
declared workloads and supported release combinations. Live bootstrap remains
unqualified and pending under the explicit deployment boundary. Public claims must follow their actual evidence, not infer a
complete performance matrix from one passing workload.

## Final reader and initialized-read observations

The [reader resource record](../dev/measurements/reader-resources.json) measures
fifty warm immutable captures after a cold open. Host descriptors stay at twenty
at every checkpoint, with one helper and zero residual leases/capture bytes or
per-read database writes. The warm sequence takes 481 ms and eight host CPU ticks;
these observations exclude engine/guest CPU.

The [initialized-read record](../dev/measurements/initialized-reads.json) contains
thirty fresh contexts on loopback and thirty with synthetic API responses delayed
by 100 ms plus serialized bytes at 10 Mbps. Conversation first useful content p95
is 89/402 ms; cached return 26/20 ms; text preview 176/273 ms. Original local/shaped
conversation, cached return and local preview targets pass. This response-delay
fixture is separate from packet RTT or a live backend SLO; the combined runtime
uses real Go APIs and its separately documented CDP profile.

## Startup comparison and instrumentation cost

The retained and current production clients load the same digest-identified
synthetic messages/files. Thirty fresh contexts per build separate navigation to
useful conversation content, initialized preview selection and cached return.
Actual 60/120-second quiet windows are separate. Baseline makes 24/48 snapshot
reads and 54/108 metadata reads; current makes zero heavy reads and 2/4 head reads.
Neither selected Markdown preview remounts in these controlled idle windows.

Sequential startup trials initially showed p95 405 ms for baseline and 1.55 seconds
for current. Adjacent paired runs with alternating order showed wide variation in
both. The final paired observation records startup p95 633/580 ms, local preview
feedback 83.4/90.9 ms and cached-return feedback 53.2/25.7 ms (baseline/current).
All runs remain in the [record](../dev/measurements/startup-comparison.json).
No uniform startup speedup or causal threefold regression is inferred. Browser
pointer-down to useful DOM on RAF is separate from Playwright action overhead;
the fixtures do not measure real network/engine latency or field INP.

An isolated Node microbenchmark alternates enabled/disabled timing-ring recording
for thirty batches of 10,000 calls. Per-call p95 is approximately 466 ns enabled
and 100 ns disabled, with the 512-entry bound checked. This measures that JavaScript
mechanism only, not browser profiling or application-wide instrumentation overhead.

## Diagnostics and removed paths

HTTP responses carry server-generated request IDs. Opt-in categorical observations
record status, bytes and duration; `Server-Timing` covers only work known before
headers. It does not include future stream transfer. Request IDs confer no authority.
Browser diagnostics are off by default; `NEXT_PUBLIC_INTERACTION_METRICS=1` enables
a bounded 512-sample category-only ring. Queue, fetch-to-headers, body/JSON parsing
and state-scheduling timings remain distinct; state scheduling is not React paint.
No bodies, paths, credentials or actors become metric labels.

Removed paths include request-time raw conversation reconstruction and its HTTP
routes/reducers, separate owner/file SSE transports, healthy broad conversation
polling, per-read mutation ledgers/helpers, refresh-key preview remounting,
duplicate transport-reply caches, schedule overview polling/accumulation, raw-scroll
rail paging, cumulative directory-page retention and journal-format-1 startup
conversion. Historical comparison code lives only in disposable qualification
checkouts and evidence; it is not a supported production fallback.
