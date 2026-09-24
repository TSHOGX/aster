# Queued-prompts acceptance record

Status: source and isolated-fixture evidence through `92ff3c2`. This is an
acceptance ledger, not a deployment sign-off. The normative rows are in the
private planning package's `03-validation.md` and the behavior specification
in `02-design.md`.

| Rows | Verified evidence | Remaining qualification |
| --- | --- | --- |
| A01-A05 | FIFO queue/count and claim/reconciliation SQLite tests; queue-only/new-chat and three-slot browser tests | Full API/live scheduling across held skip and later Release |
| A06-A10 | Active Send/Stop, Enter/IME/Shift+Enter, lost reply and draft-preservation browser tests; exact Stop receipt tests | Real two-tab reconnect and soft-keyboard device pass |
| A11-A13 | SQLite Stop/new intent serialization; narrow forced-color/dark/reduced-motion and Chinese Playwright captures | Screen-reader and touch device review |
| B01-B06 | Real Pi AgentSession fixture, patched RPC handler and pinned Khora runtime; duplicate template identity, cooperative Steer consumption, retry/compaction through runner/journal | Real partial tool-batch and arbitrary extension mutation/listener failure |
| B07-B12 | Exact target, concurrent claim, late Steer, preflight rejection, composed Stop removal and next-run non-leak | Process loss at every queued-Steer native/host boundary and arbitrary extension outcomes |
| C01-C03 | Durable receipts, prompt-local revision, typed HTTP rejection and browser same-ID retry/local row error tests | Browser reconnect with a real API and concurrent tabs |
| C04-C08 | Product recovery, journal replay, release/owner/service/capture/idle, focused race/pressure, isolated Khora fault and sustained combined load | Crash injection at every exact Steer delivery boundary |
| D01-D03 | Persisted segment/rebuild, consumption order, final ownership/coalescing and composed native Steer order tests | Full browser page/delta matrix with real native output |
| D04-D07 | Native monotonic elapsed evidence, segment clock tests, server/client frontier race and late tool update tests | Host-clock calibration and projection failure/reconnect browser path |
| D08-D10 | Navigation/page generation, shared message run/segment metadata, new-chat/lost-reply tests and exact-source historical conversion/rebuild | All share/export surfaces and additional historical import variants |

## Gates

- G1: real Session, patched handler and full stdin/stdout/runner/journal path
  pass for normal, Steer, Stop, retry and compaction. Arbitrary extension/tool
  transformations and every process-crash boundary remain unqualified.
- G2: local schema/API contract passes fresh-database tests; live format
  transition is a separate release gate.
- G3: product reducer/recovery contract and composed Stop/Steer pass; exact
  delivery crash matrix remains open.
- G4: deterministic segment/order foundation and client frontier pass focused
  tests; broad browser/export/timing qualification remains open.
- G5: automated queue/composer/browser requirements pass in light/dark,
  forced-color, reduced-motion, narrow and Chinese states; device accessibility
  review remains open.
- G6: isolated release, fault, real-browser and sustained combined suites pass;
  the remaining B/C/D rows above prevent a complete matrix claim.
- G7: release procedure, exact-source offline conversion and a staging copy,
  pinned runtime image and isolated qualification are documented and tested.
  Final executable/configuration identity and activation remain separate. No deployment.

The implementation record in [queued-prompts-implementation.md](./queued-prompts-implementation.md)
contains the staged changes and commands. Run all checks against a clean tree
and record the final commit and build identity before changing any gate status.
