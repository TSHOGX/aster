# Session and generation lifecycle

A session is durable user state; a generation is a replaceable sandbox execution
attempt. Session messages, workspace and driver homes survive generation failure,
idle release and ordinary resource reaping.

## State and input

`internal/sessionstate` defines session statuses and input admission. Turns have
independent queued/leased/running/terminal states. A generation lease and active
turn context fence bridge writes and model access. Recoverable generation errors
must not turn the whole session into an unrecoverable conversation.

- A direct start failure leaves the session retryable.
- When queued work still exists, recovery must not advertise an idle session and
  discard that work. Cold fallback can allocate a replacement generation.
- Runtime state transitions and their durable events commit together; live SSE
  and replay must converge to the same browser state.
- Stop aborts the active Pi turn and parks remaining queued prompts. A parked turn
  is not pending automatic work; it does not keep an idle generation alive.
- Sending a parked prompt can use the current generation or cold-start a new one.

## Cleanup invariants

Teardown targets a particular session, generation and runtime identity. A stale
cleanup request cannot destroy a newer generation. The runtime must delete or
prove absence of the matching container before declaring its resources destroyed.
Networking and filesystem cleanup must remain bounded and retryable.

Recursive cleanup paths must be under the configured resource root and match the
expected generation. Reject path traversal, symlink escapes and generation
mismatches. Reaping must never treat the session workspace or agent-home root as
a disposable generation directory.

Idle release frees sandbox compute without requiring a runsc memory checkpoint.
The session can retain a logical checkpointed/released status while the next turn
uses a fresh generation and persisted files. A runsc checkpoint image is a
separate path with strict runtime/contract metadata compatibility checks.

Closing a session cancels pending turns and releases its resources but preserves
recorded conversation and persistent files. It does not imply a generic disk-data
purge. Operators must consider this when sizing backup and retention storage.

## Quota and runtime projection

Session creation checks live sandbox capacity. Idle-released sessions no longer
consume a live sandbox slot. See `/api/quota` and the store live-slot query rather
than equating the number of historical sessions with current compute usage.

The runtime projection includes session, active generation, resource and turn
state. Its database revision retains the name `turn_revision`, but covers all
these dependencies through triggers. See
[runtime state synchronization](runtime-state-synchronization.md).
