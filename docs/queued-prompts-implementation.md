# Queued-prompts implementation record

This record describes the current implementation after the architecture
cleanup. The frozen planning package remains the behavior reference; this file
does not authorize deployment.

## Current authority model

The fresh schema is version 3. `turns` owns immutable input intent, queue policy,
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
Observer failures during cleanup produce unknown evidence instead of stranding
the Run. The pinned output digest is
`sha256:d700150f40b6e1f942b8082c4244cdc234948d38aa9e975b386aefb8278b9a92`.

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

Historical native input events are treated as retained display evidence only.
They preserve message chronology without being relabeled as current native
consumption. There is no old-format converter; incompatible state is removed by
the authorized destructive reset.

## Frontend contract

The queue is a bounded FIFO resource with prompt-local actions. Delivery rows
show accepted, native queued, consumed synchronization, handled, removed,
rejected and unknown outcomes with durable errors. The composer has one
active-run-driven primary action: Send while idle and Stop while active. Enter
queues an ordinary prompt; Shift+Enter inserts a newline; Stop preserves drafts.
Control request bodies retain their exact request identity through reload.

## Verification

The current repository checks pass:

- `go test ./...`
- `go test -race` focused lifecycle, claim, projection and recovery suites
- frontend Vitest: 206 passed, 1 skipped
- frontend TypeScript and ESLint checks

Former review regressions cover historical ordering, unknown-run segments,
late-tool ownership, handled inputs, listener cleanup failure and continued
segment rendering. A new immutable runtime candidate still needs to be built
and measured against protocol 4 before release qualification. Development has
not modified live data, services or runtime images.
