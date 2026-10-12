# Conversational scheduling implementation and acceptance

Scheduling spans product authority, transport, native runtime, browser surfaces,
delivery and bounded checks. The verification coverage below describes the
reproducible repository gates.

## Authority and ownership

Schedules persist future input intent. ScheduleRun rows snapshot an admitted
occurrence, definition revision and notification policy. Prompts and execution
Runs retain ordinary queue, lifecycle, recovery and outcome authority. There is
no second runner, broker, workflow engine, guest cron or journal tool protocol.

Browser and Agent adapters share transaction-level mutation operations and
immutable Project-scoped receipts. Expected definition revision fences edits;
scheduler progress changes catalog/outbox revisions without changing it. Exact
replay survives edits, cancellation and reopen. Changed payloads conflict.

The service gateway reserves all `/platform/` paths before provider dispatch.
Requests share its bounded grant/slot accounting and cross the protected owner
Unix socket. Mutation transactions verify configured capability, gateway instance,
slot sequence/open state, original grant, live Project/Session/incarnation, exact
execution Run and acquired owner snapshot. Stopping, idle, revoked, expired,
foreign and superseded authority cannot create future work. New mutations require
an open release phase. Stream continuation retains its separate drain rules.

Native tools load only through an explicit measured Pi extension. Identity hashes
logical/native Session, persisted assistant entry and provider tool-call ID,
captured before awaits. Relative time resolves once against the server clock;
retries retain identical bytes and the original expected Run. Aborted/lost replies
preserve uncertainty. Host context supplies finish identity and revision.

## Admission, delivery and presentation

One-shot consumption, Session/Prompt creation and occurrence insertion commit
atomically. Recurrence coalesces missed times and defers when an earlier occurrence
is unsettled, including unknown execution. Independent due transactions pause
invalid tasks or back off transient admission failures; global storage failure
remains a worker error. No uncertain execution is resubmitted.

Bounded interval tasks have a count/deadline, optional first-check anchor and
snapshotted delivery policy. Any scheduled input, including a consumed Steer,
restricts native management. Finish requires one unambiguous scheduled input with
the unchanged definition revision. Automatic count/deadline exhaustion permits
its already admitted live check to finish; user changes fence it. Pending checks
stay quiet; failure, unknown and budget exhaustion remain visible.

Scheduled and generic outcome delivery share `turn/<prompt_id>` IDs. Unknown
resolution updates the same notice; unchanged outcomes do not reset unread.
Count/deadline attention uses `schedule/<id>` after outstanding work settles.
No success notice is inferred from exhaustion or from the finish operation.

Browser writes use typed authored fields and stable receipts. Metadata lists,
latest-result projections, source links and visible native cards share bounded,
authorized queries and one owner invalidation stream. Cards ignore native-supplied
URLs and show tombstones after deletion. The conversation projector preserves
structured schedule receipts through ingestion and replay, and card invalidation
fences reads already in flight. Large inspected prompts appear once in native
text output rather than being duplicated into structured details. The one-shot editor handles DST gaps and
repeated offsets, and content edits preserve the exact stored instant.

Removed architecture includes schedule enabled/deleted booleans, tick-driven edit
revisions, automatic CAS rebasing, inferred save acknowledgments from current
content, unbounded list prompt bodies and legacy run-ID history continuations.
Source retirement redacts private receipt results while keeping identity tombstones.

## Verification

| Area | Evidence |
| --- | --- |
| Domain | Concurrent one-shot admission, rollback, reopen, immutable receipt replay, canonical defaults, expiry, manual consumption, CAS and source retirement tests |
| Scheduling | DST/interval phase, delayed first check, poison-row isolation, overlap/unknown suppression, count/deadline tests |
| Authority/transport | Real HTTP gateway → protected Unix socket → live Store grant; lost committed reply/replay; revoked/expired/stopping/idle/owner/slot/gateway/Run/drain/foreign fences; strict envelope/namespace bounds |
| Native | Shipped extension through the actual gateway/owner/Store with a lost committed reply; actual pinned Pi 0.77.0 loader and AgentSession; disabled discovery; native tool events; parallel identities; provider-ID reuse; immutable retry bytes; abort/late-denial uncertainty; host-bound finish |
| Runtime release | Current-manifest data asset classification, reviewed extension bytes, unsupported runtime capability rejection; tests on pinned Node 24.15.0 |
| Delivery | Generic off/both on, notify snapshots, unknown resolution, scheduled Steers, queued cancellation/handling, quiet pending checks, late finish and failure after finish |
| Browser | One-shot/bounded creation, existing recurrence editing, receipt retry, conflicts, localized/responsive forms, reconnect/windows, live card result/tombstone links and native URL rejection |
| Load | 200 due tasks across two Projects with concurrent urgent and ingestion writes; 200 admissions, no lost tasks or reserved-class rejection |

Owner/gateway diagnostics expose eligibility, oldest age, retries, errors,
outstanding prompts, admission lag and platform success/uncertainty alongside
writer wait/capacity metrics. Use the load fixtures to measure these on the target
platform; a fixture result is not a production throughput guarantee.

Repository gates include source/docs checks; backend build, vet, tests and race
suites; privileged-suite compilation; native pinned-package tests; frontend lint,
typecheck, unit tests, production build and browser tests. Terminal fixtures wait
for attachment and fail explicitly on early return.

See [verification](verification.md) for commands and test limits, and
[the current contract](compatibility.md) for schema and protocol requirements.
Installation-specific results and artifact inventories belong outside the
publishable repository, as described in [publication boundaries](delivery/privacy.md).
