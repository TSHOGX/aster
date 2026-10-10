# Conversational scheduling implementation

Implementation follows the 2026-10-10 design, with production acceptance tracked
here. Existing databases and deployments are not changed by this work.

| Stage | Status | Deliverable |
| --- | --- | --- |
| Domain and admission | Complete | Explicit lifecycle/triggers, one-shot admission, receipts, coalescing, failure isolation, typed browser writes, retirement |
| Platform transport | Complete | Reserved gateway prefix, protected owner RPC, live grant/slot/Run/release fences |
| Native runtime | Complete | Measured explicit extension, immutable request identity/retries, capability gating |
| Surfaces and delivery | Complete | One-shot editor, conversation cards/source links, history filters, scheduled inbox projection |
| Bounded checks | Pending | Count/deadline bounds, exact scheduled Run finish, quiet pending checks |
| Final qualification | Pending | Cross-layer tests, release/docs audit, legacy removal |

The foundation removes schedule `enabled`/`deleted` booleans, tick-driven edit
revisions, automatic browser CAS rebasing and legacy run-ID history cursors.
Automatic overlap is serialized per schedule; manual recurring runs remain
explicit user admissions and do not change cadence. One-shot Run-now always
consumes the existing occurrence. Receipts are private; metadata invalidation
never includes prompts or mutation values.

Schema 4 is a fresh lineage with exact fingerprint enforcement. No automatic
migration/reset is introduced. Production activation against an existing
fingerprint needs a separately reviewed data-continuity or fresh-lineage plan;
new code must never silently replace user data. Conversational tools remain
unadvertised until transport, runtime and surfaces are qualified together.

Foundation validation: `go test ./...` passes; focused schedule/admission race
tests pass; product/HTTP vet passes; frontend TypeScript and schedule unit tests
(25 cases) pass; production frontend build and schedule Playwright tests (30
cases) pass; source/documentation gates pass. Source-only retirement additionally
verifies that independent future work survives and receipt details are redacted.
Browser fixture generation uses the existing test Python virtual environment.

Platform validation exercises the actual HTTP gateway, protected Unix socket,
live product grant and committed receipt with a deliberately dropped owner
response. Identical replay returns the same receipt, changed intent conflicts,
and reserved variants never reach the model fixture. Authority cases cover
revocation, expiry, stopping, idle, owner advancement, closed slot, gateway
replacement, wrong expected Run, drain and foreign target. Slot acquisition now
snapshots the execution owner so a delayed platform call cannot cross an owner
advance, while existing model stream continuation keeps its original rules.

Platform checks: complete backend tests and vet pass; focused platform/gateway
race tests and source/documentation checks pass.

Native qualification runs the bundled extension against actual pinned Pi 0.77.0,
including explicit loader discovery restrictions and ordinary native tool events.
Independent deterministic tests cover parallel identity capture, identical-byte
retries, server-anchored relative time and abort uncertainty. The same tests run
on pinned Node 24.15.0. Manifest version 4 measures the extension as data and
refuses unsupported retained capability or changed source bytes.

Runtime checks: full backend gate (build, vet, native/product tests, privileged
suite compilation and selected fixture checks), source gates and pinned-Node
extension tests pass. No live image or deployment was changed.

Surface qualification includes one-shot creation, timezone/DST gaps and repeated
hours, receipt-backed browser retry, metadata-only live cards, native-link
validation, deleted tombstones and latest-result navigation. Scheduled delivery
covers generic-off and both-on policies, immutable notify snapshots, unknown
resolution/unread behavior, consumed steers and queued cancellation/handling.
The gateway transport fixture now waits for acknowledged slot release before
its next request; a retry denial after a lost mutation reply remains uncertain.

Surface checks: product/HTTP tests, focused race tests, frontend lint/typecheck,
233 unit tests (one pre-existing skip), production build, 31 schedule browser
cases and the live native-card browser case pass. Native retry tests also pass.
