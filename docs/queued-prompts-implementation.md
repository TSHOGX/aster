# Queued-prompts implementation progress

This record tracks implementation evidence after the planning baseline. It does not amend the frozen screenshot evidence or claim that the acceptance gates have passed.

## P0: full Session identity fixture (2026-09-24)

`orchestrator/internal/runtimerelease/full_session_identity.test.mjs` creates a real pinned Pi 0.77.0 `AgentSession` through `createAgentSession`, with an in-memory session manager, a temporary working directory, no tools, and a deterministic fake model stream. It does not call a provider. The test uses request-local context to tag the exact user message object at the `Agent.prompt` and `Agent.steer` boundaries, and observes those objects through the Session's normal subscription path.

The fixture passes for a normal prompt followed by two identical template-expanded inputs. One input uses `AgentSession.steer`; the other uses `AgentSession.prompt` with `streamingBehavior: 'steer'`. The observed consumption identities are `normal`, `A`, `B` in that order, and the stored user messages contain only the expanded text. This supports exact-message identity as an adapter mechanism even after ordinary Session event handling and persistence. It also shows why text matching cannot identify the two steer sources.

Run it with:

```sh
node --test orchestrator/internal/runtimerelease/full_session_identity.test.mjs
```

G1 remains open. The fixture has no actual RPC stdin/stdout, arbitrary extension transformations or handled commands, retry/compaction continuation, process crash, journal, or target admission gate. It does not prove a removed disposition or close the drained-input race. Those need executable qualification before the P1 wire contract is fixed.

The expanded full-session fixture now observes the Agent's `processEvents` entry point, before Session or extension listeners. It confirms that clearing `AgentSession`'s queue after the Agent has drained an input does not retract that input. It also revealed that abort alone can let `AgentSession._handlePostAgentRun` continue into a queued Steer before the prompt Promise settles. A Stop adapter therefore needs to close target admission and clear pending queues promptly, then wait for the complete prompt Promise and reconcile the identity ledger. The early clear suppresses a normal queued continuation; only the later quiescent settlement supports a definite removed disposition. The fixture exercises this sequence without claiming crash safety.

## P1 preparation: lifecycle inventory (2026-09-24)

[queued-prompts-lifecycle-inventory.md](./queued-prompts-lifecycle-inventory.md) maps current `turns.state` ownership across schema, scheduling, recovery, release, service grants, and conversation projection. It is the checklist for replacing the overloaded state with prompt, run, and delivery resources. G2 remains open until that replacement and its transaction tests are complete.

## P0: native token boundary (2026-09-24)

The pinned RPC patch is now protocol 2. Prompt and Steer commands may carry an opaque `input_token`. The adapter tags the exact user message object at the Agent boundary with a non-enumerable property, emits `input_consumed` before the ordinary user `message_start` event, and keeps the token out of model context and Session persistence. The runner validates and forwards the token without treating it as user content. The patch remains source and output hash pinned; `go test ./internal/runtimerelease` verifies idempotence and rejects unreviewed bytes.

The product store now has fresh-schema `execution_runs`, `input_deliveries`, and `control_receipts` records. Steer acceptance creates a delivery and receipt atomically; native ACK advances it to `native_queued` and does not complete the source prompt. An `input_consumed` journal event is the only path that materializes the source user message and settles that prompt. Delivery tokens are checked against the target run and are idempotent on replay. Normal prompt dispatch also creates a run and delivery record so it can move to the same evidence path.

The adapter now also emits `inputs_reconciled` after the complete Session prompt Promise settles and it clears remaining native queues. The host reducer requires a complete per-run disposition: every unresolved delivery must be consumed or listed as removed. Stop and Steer commands carry an exact run ID; both runner and native adapter reject a stale target. The runtime patch uses request-local async context and a WeakMap, so overlapping command preflights cannot swap tokens. The pinned JavaScript output is syntax-checked when Node is available.

The reconciled event now includes the native occurrence timestamp and monotonic run elapsed milliseconds. The store persists the elapsed evidence on the run. This separates execution duration from each input segment's consumed-at clock, though full clock rendering and recovery tests remain open.

The current implementation still has an ACK-created user message for ordinary prompts, because the existing conversation projector and its tests assume it. That violates the final consumed-input rule and must be removed with the coordinated P3 projection change. The runtime test exercises full Session methods, while the production RPC patch itself still needs a complete stdin/stdout integration fixture with extension, retry and crash cases. `turns.state` guards, the full receipt HTTP DTOs, source-local mutations, complete frontend integration, and release transition are also unfinished. G1–G7 remain open.

## P4: queue and composer behavior (2026-09-24)

The Session panel now reads the bounded FIFO queue resource, keeps pending prompts in the queue only, and renders three stable per-row actions. Hold/Release, Steer, and Cancel are disabled while a delivery is unresolved; native queued and unknown delivery states have explicit labels. Active runs render one Stop primary action, while idle runs render one Send action. Enter remains ordinary form submission and Shift+Enter remains newline insertion. Stop uses its own pending state, does not submit or clear the draft, and the active hint is connected with `aria-describedby`.

The New Chat pending state is now a queue submission status without a Worked-for clock. Existing browser coverage passes for lost first replies, queue-only first submission, active Stop/Enter behavior, draft preservation, and IME/Shift+Enter handling. Backend queue tests cover exact counts, FIFO pagination, Steer claim precedence over ordinary dispatch, and three-way delivery action state.

The visual/browser stage still needs the complete light/dark, reduced-motion, forced-color, narrow viewport, localization capture and real API integration qualification. P3 projection cleanup and the remaining release gates are still open.

## P1: prompt-local queue mutation receipts (2026-09-24)

Hold, Release, and Cancel now use `MutatePrompt` with a prompt-local revision and a stable request ID. The receipt is written in the same transaction as the desired-state change; retries return the original result and a reused request ID with a different action is rejected. The HTTP row-control endpoint accepts `expected_prompt_revision` and returns the receipt. Queue rows expose the latest delivery state and the frontend resynchronizes a page when its queue revision changes.

The old internal helper names remain only as test and worker adapters that translate their session-revision call shape into the new receipt path. Product transport no longer uses the session-wide revision as the row mutation guard. These adapters will be removed after the remaining worker and test call sites migrate. G2 is still open until all state guards and DTOs use the new contract directly.

## P3: consumed-input projection and segment identity (2026-09-24)

The projector now accepts the durable `input_consumed` event, creates the source prompt's user anchor from the delivery record, and closes the active run's current message group before later output. A consumed Steer therefore receives a new `segment_id`; the following assistant blocks retain the same run while joining that new segment. Existing item positions remain stable for ordinary streaming updates, while a previously imported placeholder at position zero is advanced when authoritative consumption arrives. Frontend grouping includes `segment_id` so consecutive output boundaries do not fold a later answer under the earlier prompt.

The focused projector test passes for old output → consumed Steer → subsequent output, including reload-style projection replay. Full pagination, delta, sharing/export, historical missing identities, and all timing clocks still need the broader P3/P5 qualification.

The segment reducer now closes the old run prompt's message group using the normal command's prompt ID. The consumed Steer anchor and following assistant response share one segment ID, while earlier output remains in the previous segment. The focused test asserts this membership as well as source chronology.

Conversation page seek tuples, SQL ordering/indexes, projector test ordering, and the frontend item comparator now use durable display position before block and ID. Enqueue sequence remains metadata for queue scheduling and navigation, not the transcript sort key. Existing repository and product pagination tests pass under the revised ordering.

Segment summaries now use the earliest durable `input_deliveries.consumed_at` as their worked-for start when available, falling back to the original prompt timestamp only for records without consumption evidence. This excludes queue and native-wait time from the displayed clock. The focused timing test verifies the persisted consumed timestamp is used.

## P1/P2: lifecycle guard alignment (2026-09-24)

Worker admission, Stop target selection, idle retirement, service attribution, gateway checks, and release dispatch now consult `execution_runs` alongside prompt policy. A draining release cannot create a new normal prompt command, while an already accepted Stop remains urgent. Service grants resolve the active run's prompt and drain when that prompt reaches a terminal state; a later prompt receives a fresh attribution. The product suite passes with these owner checks.

The schema still retains legacy `turns.state` for terminal outcome and compatibility adapters, and several cleanup/display paths still reference it. Full removal requires a coordinated migration of the remaining release views, recovery handlers, schedules, shares, and deletion paths. G2/G3 are not complete.

The ordinary prompt reducer still has a temporary ACK display path because the existing product fixtures and conversation projection rely on it; Steer sources already require `input_consumed`. Removing this final normal-prompt shortcut is a separate coordinated P3 change: every runner fixture and native integration must emit the normal delivery token before the old command-applied message materialization can be deleted. This remains an explicit unmet requirement.

The release schema now enforces one live `execution_runs` row per Session and derives its active/unknown release blocker from that run. Owner handoff still cross-checks the prompt's visible state and applied command, so an independently mutated prompt row cannot grant transfer authority. This is transitional until prompt policy and execution outcome have fully separate columns and the remaining `turns.state` uses are removed.

## P1 correction and P2/P3 foundation pass (2026-09-24, after `7efa9e4`)

This entry supersedes the earlier transitional claims above. The detailed current contract, transitions, limitations and continuation prerequisites are in [the private implementation handoff](../private/queued-prompts-analysis-20260924/07-implementation-handoff.md).

Prompt policy/disposition and run lifecycle are now separate. Normal dispatch and Steer use the same transactional claim, backed by a unique live-or-consumed claim index and command/run binding constraints. Execution guards have moved to runs, including service grants, release/owner handoff, recovery and idle checks. `turns.state` remains compatibility display metadata; its former unknown-acknowledgment authority has been removed. Consumption is immutable input evidence and never means the run completed.

Hold/Release/Cancel have prompt-local receipts. Steer/Stop now have explicit target run/incarnation/owner DTOs, immutable request digests, durable 202 receipts and a read endpoint for current command/delivery/run evidence. Failed admission returns a typed `not_admitted` error. The frontend sends the new target contract, uses canonical `hold`/`stop` actions and retains unresolved control request identities in memory. Existing Send/Stop, Enter and draft-preservation behavior remains covered by browser checks.

Both normal and Steer inputs materialize only on consumption. Reconciliation distinguishes proven host non-dispatch from native removal and uncertain in-flight deliveries, allowing later journal evidence to advance. Late rejection can close a completion or rejection barrier; preflight rejection no longer strands a run. Process-loss and send-uncertainty paths preserve consumption, and captures are keyed by immutable per-run path so an explicitly retried unconsumed prompt cannot collide with its previous attempt. Lifecycle/segment tables participate in deletion cleanup.

Native protocol 3 closes target admission around asynchronous Steer preflight, waits for admitted preflights, then clears/reconciles before releasing the run. The pinned output hash changed, as did the schema fingerprint and owner-handoff protocol (2). No installed runtime, live database or deployment was modified.

P3 now persists segment membership across batches/rebuilds, coalesces consecutive inputs before work, preserves item membership on updates, assigns subsequent answers to their segment, and stores native elapsed differences for segment clocks. Earlier segments close as `continued`. Navigation follows durable message positions. Historical missing consumption time does not acquire a fabricated start. Full client-frontier queue handoff and all share/export/clock-calibration acceptance remain open.

Validation for this pass:

- `go -C orchestrator test ./...`: passed after contract fixture updates, including real SQLite transaction/reducer tests and existing release/recovery/cleanup suites.
- Focused `go test -race ./internal/product -run ...` runs passed for the changed claim, control, recovery, consumption, segment/rebuild and service-attribution contracts. The earlier broad race-instrumented suite was stopped after subsequent contract edits; no whole-suite race acceptance is claimed.
- `npm --prefix frontend run typecheck`, `npm --prefix frontend run lint`, `npm --prefix frontend test`: passed; 202 unit tests passed, one existing test skipped.
- Five selected Playwright UI tests passed: lost first-send response, queue-only initial prompt, active Stop with Enter queueing/draft preservation, completed-answer actions/timing, and composer keyboard/IME behavior. The run includes a production frontend build and uses API fixtures.
- Three pinned full-AgentSession tests passed with deterministic model streams.
- Five new tests execute the actual patched RPC handler with Session/transport doubles: delayed preflight after natural settlement, Stop removal, duplicate-text identity/elapsed evidence, handled input, and rejected preflight. These are included in the runtime Go suite.
- Additional contract coverage includes concurrent claim/cancel, ACK without materialization, consumption before ACK/unknown, retry after settlement/drain, both pending and in-flight late Steers, rejection with an in-flight control, explicit retry capture retention, cleanup integrity, and multi-batch/rebuilt segment identity and durations.

P1's local contract implementation is established; native qualification remains a prerequisite to claiming G2 acceptance. P2 and P3 have a tested foundation, with the remaining G1/G3/G4 acceptance work explicitly listed in the handoff. Mocked handler/browser tests and happy paths do not close those gates. P4–P6 are not declared complete.

Service gateway slots and in-memory attribution now retain the exact run ID, so a new attempt of the same prompt cannot inherit an earlier upstream request. Stop can quiesce its exact unknown run even while the Session is blocked. Additional regressions cover both cases and ACK resolution of acceptance uncertainty without resurrecting removed input.

## Exact control API cleanup (2026-09-24)

The old `Steer`, `Interrupt`, `ReleaseTurn` and `ChangeQueuedTurn` product methods have been removed. Product tests now submit `ExecutionControl` with the original run/incarnation/owner fence or call `MutatePrompt` with a prompt-local revision. A replay test retains the original Steer request body after consumption instead of reconstructing it from mutable Session state. This removes the last session-wide control-revision adapter from the Go product API; the control revision remains a read refresh marker. The display-only `turns.state` cache remains the next schema cleanup stage.

## Derived prompt status (2026-09-24)

The fresh schema no longer stores `turns.state`. Queue policy and disposition determine pending, claimed and canceled input status; a correlated execution run supplies starting, running, stopping and terminal status. Normal prompts can therefore show their run state before native input consumption, while an unconsumed Steer source stays claimed. Control revision changes on prompt and run transitions. Notifications follow run outcome evidence. Schedule status, activity blockers, title candidates, summaries and navigation read their respective prompt/run/segment authorities. Historical display tests now provide explicit segment metadata instead of mutating a synthetic turn outcome. No live database was converted.
