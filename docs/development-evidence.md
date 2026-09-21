# Development evidence

This record covers development correctness, not production migration or deployment
acceptance. Use the [verification commands](verification.md) and
[isolated harness](../dev/README.md) to reproduce it. The harness records source
revision, dirty status, engine binary hash and both raw/canonical manifest hashes.

## Implemented paths

| Area | Executed evidence |
|---|---|
| Generic connectivity | Real TCP and UDP allows; live undeclared destinations and direct gateway/upstream access denied; managed model traffic steered successfully |
| Gateway lifetime | Separate UID/process, process restart, original-grant expiry during authorization outage, interrupt/complete drains, slow body/response transport tests |
| Retained execution | Lost operation replies, owner replacement, start recovery, repeated generations, journal replay, native restore and retained retirement |
| Capture/projection | Independent failure backoff, ordered projection CAS, capture process kill/restart, exact archive/receipt hashes |
| Operations | Read-only diagnostics command, administrator-only API, stale worker/owner tests, bounded continuation and supervised workers |
| Frontend baseline | Lint/typecheck, 228 unit tests, production build, six fixture journeys, and a real Chromium daemon journey |

The fixture tests use API interception; the real daemon journey separately runs
Chromium through the Next proxy against the daemon and real shell session. The daemon
journey verified login, capability-selected shell creation, streamed output, reload
persistence, workspace retrieval, interruption, and three SSE connections. The same
real product run verified cookie/session persistence across daemon restart, admin and
owner boundaries, reader/public share revocation, and SSE replay. A composed real Pi
run verified two scheduled existing-session turns after worker restart and rejected
foreign schedule targets.

## Finite retained workload

The initial measurement at application revision `95eafe9` used engine `e8cd021a9c7c`
and canonical template manifest
`073e3ed4769ec726af4ad6fc91e619a2b6829eaab8052f6c46b757e261bf1ab4`.
The workload/harness source is committed with this record.

- Duration: at least 60 seconds; two concurrent shell sessions and two execution slots.
- Each normal turn sleeps one second and emits 32 KiB plus a unique marker.
- An owner/application runtime replacement occurs while two eight-second turns run.
- Every fourth round pauses both sessions; later rounds reuse their retained storage.
- Exact workspace counters and committed message identities must match accepted turns.
- Final pause must reduce occupied/running execution capacity to zero.

The measured baseline completed 68 turns in 62.1 seconds, with 16 intermediate
pause/resume cycles and 2,229,636 verified committed assistant-history bytes. Median ordinary-turn latency was 1.21 seconds and p95 was
2.38 seconds, including the intentional one-second sleep. Allocated product database
pages grew by approximately 12.0 MiB. Retained configured overlay capacity was
512 MiB and session volume capacity was 256 MiB across the two sessions. These
capacity figures are reservations; they are not physical CoW byte measurements.
The [machine-readable result](../dev/measurements/retained-2026-09-21.json) records
the exact inputs and dirty-source status; the final tagged suite below is the revision validation gate.

Native history/capture and gateway outages use separate real tests because those
roles have different deadlines and failure behavior. The composed model test
includes an actual grant-expiry wait and therefore takes roughly two minutes.

## Limits

Retained user data, immutable history and reserved network tuples intentionally
accumulate. The tuple pool never recycles identities. Storage/tuple diagnostics and
configured caps define finite operating limits; this evidence does not promise
unbounded history or storage. Prepared uploads are collected separately from user
storage. Host/storage-crash guarantees remain those of the pinned engine and the
archive/publication durability boundaries; process-restart evidence alone does not
establish power-loss durability.

## Final product journey

At application revision `7608a31`, the public harness ran the real daemon journey with
Chromium and the pinned engine. It passed the shell workspace, streaming, interruption,
restart, authentication, sharing, public frozen files, SSE replay and browser checks.
The composed gateway test also passed real Pi scheduling through the daemon: two queued
manual runs completed as `succeeded` after maintenance worker restart, while foreign
users and targets were rejected.
