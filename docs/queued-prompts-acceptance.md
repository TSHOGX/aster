# Queued-prompts acceptance record

Status: source and isolated-fixture evidence through `9ea3d2f`. This is an
acceptance ledger, not a deployment sign-off. The normative rows are in the
private planning package's `03-validation.md` and the behavior specification
in `02-design.md`.

| Rows | Verified evidence | Remaining qualification |
| --- | --- | --- |
| A01-A05 | FIFO queue/count and claim/reconciliation SQLite tests; queue-only/new-chat and three-slot browser tests | Full API/live scheduling across held skip and later Release |
| A06-A10 | Active Send/Stop, Enter/IME/Shift+Enter, lost reply and draft-preservation browser tests; exact Stop receipt tests | Real two-tab reconnect and soft-keyboard device pass |
| A11-A13 | SQLite Stop/new intent serialization; narrow forced-color/dark/reduced-motion and Chinese Playwright captures | Screen-reader and touch device review |
| B01-B06 | Real Pi AgentSession fixture and actual patched RPC handler with fake streams; duplicate template identity, ACK-before-consumption and retry/compaction runner settlement tests | Real tool-batch, extension mutation/listener failure, retry/compaction through full RPC and journal |
| B07-B12 | Exact target, concurrent claim, late Steer, Stop removal, preflight rejection and next-run non-leak tests | Process loss at every native/host boundary and arbitrary extension outcomes |
| C01-C03 | Durable receipts, prompt-local revision, typed HTTP rejection and browser same-ID retry/local row error tests | Browser reconnect with a real API and concurrent tabs |
| C04-C08 | Product recovery, journal replay, release/owner/service/capture/idle and focused race/pressure tests | Combined process-crash fault matrix and sustained integrated load |
| D01-D03 | Persisted segment/rebuild, consumption order, final ownership and coalescing tests | Full browser page/delta matrix with real native output |
| D04-D07 | Native monotonic elapsed evidence, segment clock tests, server/client frontier race and late tool update tests | Host-clock calibration and projection failure/reconnect browser path |
| D08-D10 | Navigation/page generation, shared message run/segment metadata, new-chat/lost-reply tests and exact-source historical conversion/rebuild | All share/export surfaces and additional historical import variants |

## Gates

- G1: partial. Full AgentSession plus patched handler is covered, but not the
  complete process/stdin/stdout/journal crash pipeline.
- G2: local schema/API contract passes fresh-database tests; live format
  transition is a separate release gate.
- G3: product reducer/recovery contract passes focused tests; combined failure
  matrix remains open.
- G4: deterministic segment/order foundation and client frontier pass focused
  tests; broad browser/export/timing qualification remains open.
- G5: automated queue/composer browser requirements pass; device accessibility
  review remains open.
- G6: not closed until the integrated and fault scenarios above pass.
- G7: release procedure, exact-source offline converter and a staging conversion
  of a consistent incumbent copy are documented and tested. Final candidate
  identity and integrated release qualification remain open. No activation.

The implementation record in [queued-prompts-implementation.md](./queued-prompts-implementation.md)
contains the staged changes and commands. Run all checks against a clean tree
and record the final commit and build identity before changing any gate status.
