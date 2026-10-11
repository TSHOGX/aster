# Conversational scheduling implementation and acceptance

The implementation completes the 2026-10-10 scheduling design across product,
transport, native runtime, browser surfaces, delivery and bounded checks.
Fresh development deployment qualification follows the source gates below.

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
| Runtime release | Manifest-4 data asset classification, reviewed extension bytes, unsupported runtime capability rejection; tests on pinned Node 24.15.0 |
| Delivery | Generic off/both on, notify snapshots, unknown resolution, scheduled Steers, queued cancellation/handling, quiet pending checks, late finish and failure after finish |
| Browser | One-shot/bounded creation, existing recurrence editing, receipt retry, conflicts, localized/responsive forms, reconnect/windows, live card result/tombstone links and native URL rejection |
| Load | 200 due tasks across two Projects with concurrent urgent and ingestion writes; 200 admissions, no lost tasks or reserved-class rejection |

A local load-fixture sample admitted 200 due tasks in approximately 263 ms with
maximum urgent writer wait about 1.4 ms. The deterministic due clock was one
minute late. These are local fixture observations, not production throughput or
start-time guarantees. Existing owner/gateway diagnostics expose eligibility,
oldest age, retries, errors, outstanding prompts, admission lag and platform
success/uncertainty alongside writer wait/capacity metrics.

Repository gates include source/docs checks; backend build, vet, tests and race
suites; privileged-suite compilation; native pinned-package tests; frontend lint,
typecheck, unit tests, production build and browser tests. An existing terminal
fixture readiness race was fixed so input tests await the attachment and fail
explicitly on early return instead of hanging.

Final qualification results:

- Source/docs/lockfile gates, backend build/vet/tests, privileged-suite compilation
  and the selected nonprivileged integration fixtures passed.
- Full product/runner/runtime race suites passed; the final scheduling, platform,
  gateway and projection changes also passed focused race checks.
- Frontend lint/typecheck, 234 unit tests and production build passed. One existing
  opt-in unit test remains skipped.
- The broad browser run passed 203 cases and skipped 28 opt-in cases. Its four
  failures were picker assertions for the retired ordering; after updating them
  for the new Once option, all 41 affected schedule/card/picker cases passed.
  Together these cover all 207 active cases in that browser suite.
- Six native scheduling tests passed on pinned Node 24.15.0, including a large
  escaped-prompt result bound, and actual Pi loader/tool-loop qualification.


## Fresh deployment qualification

The 2026-10-10 development reset installed the verified candidate and a fresh
OS/Pi runtime built from the pinned inputs. Product and engine state, owned
volumes and obsolete runtime templates were replaced. Credential, TLS and reviewed
content hashes were identical after removing the temporary smoke login.
The runtime catalog has been removed: all Projects must match the single current
runtime binding. Supervised services use the same candidate.

The isolated release suite passed against the candidate's actual executables and
packaged frontend. Real Pi called create/list/get/update/cancel through the guest
service route; automatic one-shot admission produced a durable result notice, and
a new-Session bounded check finished through host-bound authority. The suites also
passed owner handoff/outage, native retries and compaction, queued input, network
policy changes, paused Files, real browsers, realtime invalidation and fault recovery.

A five-minute mixed workload completed 60 rounds with HTTP p95 approximately
6.8 ms, completion-to-display p95 390 ms and maximum combined owner/gateway RSS
63 MiB. These are local qualification measurements, not production guarantees.
The workload repaired its intentionally impaired capture lane, met fairness bounds
and reclaimed its resources.

The installed-service browser smoke authenticated, submitted a natural-language
request to the configured model, observed its successful native scheduling receipt
and persisted card, then waited for automatic execution and the Inbox result.
Disposable Projects and the temporary login were removed. Only retired Project
and Session tombstones remain; no active Project or scheduled work remains.
Database integrity and foreign-key checks passed.

Qualification corrected three test fixtures: a prompt-state SQL query now reads
execution authority, workload deletion supplies its required request ID, and file
watching waits for the initial baseline before generating changes. The final
file-watch test correction and deployment documentation do not change the
installed candidate's executable code. Exact source and artifact identities are
recorded in the [sanitized deployment record](../dev/measurements/current-deployment.json).

Physical power-loss behavior and the optional combined-load and external-provider
handoff suites were not exercised. The installed smoke verifies actual model tool
selection for one scheduling request; deterministic native tests cover the full
tool lifecycle. The release requires schema 5, manifest 5, native protocol 4 and
schedule protocol 1. Old databases/manifests are refused; there is no migration or
compatibility adapter. Follow [delivery](delivery.md) and [bootstrap](bootstrap.md).
