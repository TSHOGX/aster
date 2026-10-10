# Conversational scheduling implementation

Implementation follows the 2026-10-10 design, with production acceptance tracked
here. Existing databases and deployments are not changed by this work.

| Stage | Status | Deliverable |
| --- | --- | --- |
| Domain and admission | Complete | Explicit lifecycle/triggers, one-shot admission, receipts, coalescing, failure isolation, typed browser writes, retirement |
| Platform transport | Pending | Reserved gateway prefix, protected owner RPC, live grant/slot/Run/release fences |
| Native runtime | Pending | Measured explicit extension, immutable request identity/retries, capability gating |
| Surfaces and delivery | Pending | One-shot editor, conversation cards/source links, history filters, scheduled inbox projection |
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
