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

Segment summaries now use the earliest durable `input_deliveries.consumed_at` as their worked-for start when available, falling back to the original prompt timestamp only for records without consumption evidence. This excludes queue and native-wait time from the displayed clock. The focused timing test verifies the persisted consumed timestamp is used.

## P1/P2: lifecycle guard alignment (2026-09-24)

Worker admission, Stop target selection, idle retirement, service attribution, gateway checks, and release dispatch now consult `execution_runs` alongside prompt policy. A draining release cannot create a new normal prompt command, while an already accepted Stop remains urgent. Service grants resolve the active run's prompt and drain when that prompt reaches a terminal state; a later prompt receives a fresh attribution. The product suite passes with these owner checks.

The schema still retains legacy `turns.state` for terminal outcome and compatibility adapters, and several cleanup/display paths still reference it. Full removal requires a coordinated migration of the remaining release views, recovery handlers, schedules, shares, and deletion paths. G2/G3 are not complete.
