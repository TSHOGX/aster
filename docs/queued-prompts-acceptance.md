# Queued-prompts acceptance record

Status: historical development acceptance through `50fda5e`; it is superseded
for the current protocol-4/schema-3 work and is not a deployment sign-off. The normative rows are in the
private planning package's `03-validation.md` and the behavior specification
in `02-design.md`.

| Rows | Verified evidence | Residual limit |
| --- | --- | --- |
| A01-A05 | FIFO queue/count, held-skip/later-Release A,C,B dispatch and projection, claim/reconciliation SQLite tests; queue-only/new-chat, three-slot and real-browser held-row Steer | None in the implemented queue path |
| A06-A10 | Active Send/Stop, stopping/recovery-disabled action after reload, Enter/IME/Shift+Enter, lost reply, draft preservation, two-tab Steer reconnect and exact Stop receipts | Device-specific soft-keyboard differences are not simulated |
| A11-A13 | SQLite Stop/new intent serialization; keyboard/focus, mobile touch menu, forced-color/dark/reduced-motion and Chinese browser captures | No physical screen-reader session was run |
| B01-B06 | Real Pi AgentSession and patched RPC with duplicate templates, two-tool batch and listener failure; pinned Khora cooperative Steer, retry/compaction through runner/journal | Unknown third-party extensions cannot be exhaustively enumerated |
| B07-B12 | Exact target, concurrent claim, late Steer, preflight rejection, composed Stop removal, worker loss after acceptance and next-run non-leak | Other crash points are qualified by reducer/journal tests rather than one composed test per point |
| C01-C03 | Durable receipts, prompt-local revision, typed HTTP rejection, browser same-ID retry/local row error and real API two-tab Steer reconnect | No known authority gap |
| C04-C08 | Store/runner crash and replay cases, release/owner/service/capture/idle, focused race/pressure, composed Khora worker loss and sustained combined load | Fault permutations are bounded by the tested durable identity/state invariants |
| D01-D03 | Persisted segment/rebuild, consumption order, final ownership/coalescing, page/delta tests and composed native Steer order | No known chronology gap |
| D04-D07 | Native monotonic elapsed, segment clocks, server/client/off-window frontier races, real-browser two-tab handoff and late tool updates | Running wall-clock label is provisional; terminal duration uses native evidence |
| D08-D10 | Navigation/page generation, shared/granted message run/segment metadata, new-chat/lost-reply tests and exact-source historical conversion/rebuild | Historical inputs without native consumption evidence have no precise clock |

## Gates

- G1: passed for the pinned Session/RPC and full runner/journal path. Exact
  identity, complete tool-batch timing and native settlement are verified.
- G2: passed for the fresh schema, API receipts, one run authority and one
  delivery claim path. The old-format copy transition is qualified separately.
- G3: passed for controls and recovery. Consumption, removal and unknown are
  distinct, with no duplicate delivery or next-run leak in component and
  composed fault tests.
- G4: passed for current-format projection, queue handoff, navigation and
  sharing chronology. Historical missing evidence is explicitly unavailable.
- G5: passed for automated keyboard, touch, narrow, locale and accessibility
  semantics. A physical assistive-device review remains a release usability check.
- G6: passed in isolated native, release, fault, real-browser and sustained
  combined suites. The residual limits above do not weaken the durable
  consumption, chronology or unknown-outcome invariants.
- G7: release preparation passed on an exact-source staging copy and pinned
  candidate runtime. Staging `release-identity` measured executable, schema,
  runtime and content digests. Final `/etc` identity, old-sandbox retirement
  and activation require separate release authorization. No deployment.

The implementation record in [queued-prompts-implementation.md](./queued-prompts-implementation.md)
contains the staged changes and commands. Final release checks must remeasure
the incumbent and candidate after the authorized quiescent drain.

The 2026-09-25 architecture correction reopens G1/G3/G4/G5/G6. The current
contract now distinguishes extension handled inputs, exception-safe native
settlement, unknown-run segment presentation and historical input provenance;
the previous protocol-3 candidate evidence does not qualify these changes.
