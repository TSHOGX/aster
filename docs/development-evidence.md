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
| Frontend baseline | Lint/typecheck, 228 unit tests, production build and six browser fixture correctness journeys |

Tests with synthetic API interception are frontend evidence; they do not prove a
browser-to-daemon journey. The remaining product/browser and final revision audit
are tracked separately until executed.

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
the exact inputs and dirty-source status; final overall revision validation is
still required.

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
