# Queued-prompts implementation record

This record describes the current implementation after the architecture
cleanup. The frozen planning package remains the behavior reference; this file
does not authorize deployment.

## Current authority model

The fresh schema is version 4. `turns` owns immutable input intent, queue policy,
prompt revision and disposition. `input_deliveries` owns the exact native token
and monotonic delivery evidence. `execution_runs` owns execution lifecycle,
owner/incarnation fencing, reconciliation and outcome. `control_receipts` owns
idempotent admission. `conversation_segments` and the display tables are
derived presentation state. No persisted `turns.state` or session-wide control
adapter remains.

Normal dispatch and Steer use the same transactional claim helper. Stop and
Steer carry the exact target Run fence. Unknown deliveries remain exclusive;
only proven removed or rejected inputs return to held. Consumed and handled
inputs are terminal and cannot be redispatched.

## Native protocol 4

The pinned RPC adapter tags exact Agent message objects with request-local
delivery tokens. It emits `input_consumed` before the ordinary user event and
keeps tokens out of model content and Session persistence. Successful extension
handling emits `inputs_handled`, which closes the intent without creating a
model transcript anchor. Settlement is idempotent and joins admitted
preflights, clears native queues, emits reconciliation and releases the target.
Queue observer failures are diagnostic after core cleanup succeeds. A core
cleanup failure fences native admission and terminates the original process for
journal recovery; it never asserts complete reconciliation. The pinned output digest is
`sha256:e533d9e5bbe59991460ca018b123fab699554918ca95ba5c8a860642f19a0952`.

Runner validation binds Steer/Stop frames to the active Run. The host reducer
accepts reordered ACK and consumption facts, rejects contradictory terminal
evidence, and retains unknown outcomes across process loss and owner changes.

## Projection and timing

Display order uses durable source positions. Consumption creates input anchors;
handled inputs do not. Consecutive inputs before work share a segment. Item
updates resolve their existing Segment before changing work ownership, so late
tool results cannot split or contaminate a later segment. Run loss changes live
segment presentation to unknown without inventing elapsed time; continued
segments remain successful boundaries. Rebuild uses the same evidence and emits
no native commands.

Historical input materialization and unmatched-message fallback projection are
removed. Only current correlated native consumption can create transcript input.
The schema has no historical disposition or import cursor. Incompatible state
is discarded during the authorized reset.

## Frontend contract

The queue is a bounded FIFO resource with prompt-local actions. Delivery rows
show accepted, native queued, consumed synchronization, handled, removed,
rejected and unknown outcomes with durable errors. The composer has one
active-run-driven primary action: Send while idle and Stop while active. Enter
queues an ordinary prompt; Shift+Enter inserts a newline; Stop preserves drafts.
Control request bodies retain their exact request identity through reload.

## Verification

Verification covers lifecycle, claims, projection and recovery through ordinary
Go tests and focused race suites, plus frontend unit, TypeScript and ESLint checks.
Regression cases cover historical ordering, unknown-run segments, late-tool
ownership, handled inputs, listener cleanup failure and continued segment rendering.
The runtime release, network and browser gates exercise the built artifacts.
See [verification](verification.md) for commands, coverage and limits.
