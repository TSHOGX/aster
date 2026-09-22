# Interaction architecture implementation

This ledger distinguishes implemented behavior from remaining refactor work.
Measurements from synthetic fixtures do not qualify production performance or
active-model deployment continuity.

| Stage | Status | Scope |
| --- | --- | --- |
| P01a | Verified | Truthful initial conversation loading; bounded opt-in request timings |
| P01b–e | Pending | Shared authenticated resources, file invalidation, stable previews, lazy rendering, comparison |
| P02 | Pending | Schema migration and durable bounded conversation views |
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
