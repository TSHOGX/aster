# Development evidence

These are historical development runs, not a production acceptance report or a
claim that every later revision has been tested. Reproduce them with
[verification](verification.md) and the [isolated harness](../dev/README.md).
Raw logs remain private; the records below retain synthetic inputs and measurements.

## Recorded revisions and scope

All engine runs here used Khora module
`v0.0.2-0.20260920024301-e8cd021a9c7c` and canonical template digest
`073e3ed4769ec726af4ad6fc91e619a2b6829eaab8052f6c46b757e261bf1ab4`.

| Run | Recorded source | Result and scope |
|---|---|---|
| Initial sustained workload | `95eafe9`, dirty tree | 68 shell turns; [measurement](../dev/measurements/retained-2026-09-21.json) |
| Real daemon with Chromium | `7a7c5b8`, dirty tree containing the product-journey changes later committed in `7608a31` | Login, shell creation/output, reload, workspace read, Stop/reconnect; three SSE connections |
| Complete tagged suite | `cd49e6d`, clean tree | All 11 selected `TestKhora*` tests passed, including daemon product and composed schedule journeys; browser option was not enabled |
| Ordinary source/frontend checks during finalization | Refactor source plus the product-stage changes | Go tests and races, guest Python tests, frontend lint/typecheck, 228 Vitest tests, build and six Playwright correctness fixtures passed |

The browser run was previously attributed to later documentation commits. Its
actual harness metadata names `7a7c5b8` with `dirty: true`; it is evidence for that
working tree, not a clean checkout of `72d0199`. Browser Stop validation reloads the
page before waiting for Run to return. It proves interruption/reconnect behavior,
not immediate control-state convergence without a reload.

The six Playwright fixtures intercept API/SSE traffic. The real Chromium journey
uses browser → Next proxy → launched daemon → real Khora shell with no interception.
Pi scheduling runs through HTTP on the composed server and real model gateway; it
is not a browser scheduling journey or a separate `orchestrator serve` process.

## Complete tagged suite

The clean run at `cd49e6d` executed:

| Test | Evidence |
|---|---|
| `TestKhoraTemplateShellAndNativeHistory` | Finalized template, shell process and native history contract |
| `TestKhoraExecutionWorkerCommitsTurnMessagesAndJournal` | Shell HTTP dispatch, journal/message commits, adoption and control faults |
| `TestKhoraPiExecutionWorkerCommitsNativeState` | Pi native state, restore, archive receipts and recovery |
| `TestKhoraInterruptedRecoveryFlushesMissingCompletion` | Historical journal recovery when the live completion was missed |
| `TestKhoraComposedModelGatewayCarriesRealTraffic` | Real Pi traffic, lost start reply, gateway process restart, auth rejection, interrupt and original-grant expiry during authorization outage |
| `TestKhoraNetworkBoundaryUsesDeclaredTCPAndUDP` | Direct TCP/UDP allow/deny, managed route checks, two scheduled Pi runs across worker restart, capture-process restart |
| `TestKhoraApplicationCompositionServesHTTPWithoutEmbeddedRuntime` | Composed retained HTTP lifecycle/workspace behavior |
| `TestKhoraContentPublicationSurvivesInterruptedPreparation` | Publication retry after interruption |
| `TestKhoraContentPublicationRefusesContradictoryRevision` | Refusal of contradictory authored revision evidence |
| `TestKhoraSustainedRetainedWorkload` | Concurrent history/workspace preservation, owner restart, pause/resume and released capacity |
| `TestKhoraDaemonStartsWithoutEmbeddedRuntime` | Launched daemon, authored assets, auth/admin/owner boundaries, identity/public share revocation, frozen file bytes, SSE replay, restart and retirement |

The Pi fault fixture advances its persisted retry deadline after injecting a lost
capture receipt so the focused test does not wait through production backoff. Unit
and store tests separately exercise that backoff. Composed gateway fixtures use
distinct authored revisions: deleting one fixture's volume must not leave another
fixture reusing its engine idempotency identity.

## Finite retained workload

Normal turns sleep one second and emit 32 KiB plus a marker in each of two shell
sessions. The owner/runtime is replaced while two eight-second turns run. Every
fourth round pauses both sessions. Exact workspace counters and committed message
identities must match accepted turns; the final pause must release execution capacity.

| Measurement | Initial run | Final clean run |
|---|---:|---:|
| Duration | 62.07 s | 61.11 s |
| Completed turns | 68 | 66 |
| Concurrent sessions | 2 | 2 |
| Pause/resume cycles | 16 | 16 |
| Owner restarts | 1 | 1 |
| Median ordinary-turn latency | 1.207 s | 1.218 s |
| p95 ordinary-turn latency | 2.376 s | 2.593 s |
| Verified committed history | 2,229,636 bytes | 2,164,058 bytes |
| Database allocation growth | 12,599,296 bytes (12.02 MiB) | 11,870,208 bytes (11.32 MiB) |
| Final running placements/turns | 0 / 0 | 0 / 0 |

Latency includes the intentional one-second sleep. Each run reserves 512 MiB of
retained overlay and 256 MiB of session-volume capacity across the two sessions;
these are configured capacities, not physical CoW usage. The final machine-readable
[measurement](../dev/measurements/retained-final-2026-09-21.json) preserves exact
inputs and results. Native capture uses separate Pi tests; the shell workload's
zero native archive bytes do not imply Pi history has zero storage cost.

## Limits and current source boundaries

Retained user data, immutable history and reserved network tuples accumulate.
The tuple pool never recycles identities. Diagnostics and configured caps establish
finite limits; these runs do not promise unbounded storage or history. Prepared
uploads are collected separately from retained storage.

Process restart and exact archive checks do not establish power-loss durability.
Host/storage-crash behavior follows the pinned engine and publication/archive sync
boundaries. Production migration and deployment remain a separate project.

The redundant WebSocket event route was removed. The source still contains unpaged
frontend/API fallbacks and build-time page switches, documented in
[frontend](frontend.md). A passing integration suite does not prove that all
compatibility code has been removed.
