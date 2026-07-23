# ADR: Session and Runtime Lifetime Separation

> Type: decision record / durable invariant · Status: implemented and load-bearing.
> This is the lifetime contract that later work must preserve. System overview:
> [`../architecture.md`](../architecture.md); control-plane internals:
> [`../control-plane.md`](../control-plane.md).

## Principle

User sessions, conversation history, and workspace files persist **independently**
of live gVisor runtime resources (sandbox processes, netns/veth, checkpoint images,
`/30` slots). A runtime generation can be retired, cleaned, and replaced without
making its owning session terminal. Session lifetime is governed by
`harness.session_retention` (`0s` = never auto-expire); generation idle lifetime by
`harness.checkpoint.idle_threshold`; checkpoint-image lifetime by
`harness.reaper.checkpoint_image_retention` — three independent dials.

## Durable invariants

Any change to the control plane must keep all of these true.

### Data outlives runtime

- `messages` and `artifacts` rows are never deleted by sweep/reaper.
- Session workspace and per-(session, driver) agent-home directories are never
  touched by sweep/reaper.
- `MonitorIdleSessions` acts only on generations, never on session data.
- `failed_retention` is generation-scoped; checkpoint-image retirement has its own
  retention knob. Event-log retention (`harness.events.retention_*`) is independent
  of resumability — resuming a session needs only `messages` + workspace.

### Generation failure ≠ session failure

A generation start failure (probe failure, manifest mismatch, OCI/bundle error,
restore fallback) retires **only that generation**; the session stays retryable.

- Direct pre-enqueue failure (before a queued turn exists): mark the generation
  `failed`, move the session to an input-acceptable non-checkpointed status
  (`created` if no runtime ever succeeded, else `running_idle`), so the next message
  cold-starts a new generation.
- Background cold-fallback failure (queued turns already exist): mark the replacement
  generation `failed` but keep the session non-terminal and **input-blocking**
  (normally `running_active`) until maintenance starts a replacement or cancels the
  turns. Never publish `running_idle` while queued work remains.
- Only a genuine session-level invariant violation (corrupt row, impossible CAS) may
  call `FailSession`.
- **Event contract:** retryable failures publish a non-terminal event
  (`generation.error` / `session.runtime_error` with `{terminal:false, …}`), never
  `session.failed` / terminal `session.error`. The state transition and its durable
  event are appended in the **same store transaction**, which returns the committed
  event IDs; the server publishes only from those IDs. Live SSE and durable replay
  must reduce identically, so any event that changes user-visible state must carry
  every field the reducer applies (e.g. a checkpoint-retirement event must clear
  `checkpoint_path` / `restore_ms`, not just set `status`).

### Runtime teardown is generation-fenced

Any path that will mark a generation retryable or reclaimable after a live/restored
container may exist must first target `{session_id, generation_id, runtime_id}`:
synchronously cancel/close/evict the in-memory `containers[session_id]` entry **and**
delete or prove-absence of the runsc container before mutating DB rows. The hot start
path may reuse a container only when its `GenerationID` matches the request; on
mismatch it must tear down or return a typed stale-runtime error, never write to
stale stdin. Runsc deletes use the actual restore/container ID
(`phase3-<sessionID>` today), never the bare session ID. If absence cannot be proven,
abort and leave rows non-reclaimable rather than recording a live sandbox as gone.

### Start is lease-resilient

A generation start renews its owned start lease across all long phases (prepare,
artifact digests, live-marking, `Runtime.Start` incl. probe/restore). The renewal
guard keeps the lease alive for both success and failure, stops after the terminal
transition, and treats owner loss as stale ownership. The retryable start-failure
helper is the only non-recovery path allowed to finalize under an expired lease, and
only when `lease_owner`, active generation, and lifecycle attempt are all unchanged.

### Bridge turn completion is a turn outcome, not a session state

A failed/canceled `ack_turn_completed` marks the turn terminal; it does **not** fail
the session. `CompleteTurn` records the turn, appends the durable completion event,
and runs a guarded session-idle CAS in the same transaction: it moves the session to
`running_idle` only when the completing generation is still active and no
queued/leased/running turns remain, setting `last_activity_at`/`updated_at` to
completion time (so checkpoint eligibility measures from completion, not submit).
Duplicate/stale/racing completions are no-ops and must not rewrite `last_activity_at`
or re-publish idle. A crash after `CompleteTurn` commits but before server-side
publication must leave the session already input-acceptable and replayable.

### Restore fallback mirrors checkpoint retirement

When restore fails after `ClaimCheckpointedGenerationForRestore`, one transaction:
fails the claimed generation, moves allocation/resource rows to `reclaimable`, sets
the session to an input-acceptable non-checkpointed status, clears all API-visible
checkpoint/restore metadata, and appends the durable retirement + status events.
Live-runtime teardown (by restore/container ID) must complete before those rows are
marked reclaimable. If replacement allocation then fails, the session stays
non-checkpointed and retryable — never stuck `checkpointed` behind a failed
generation.

### Expired-runtime recovery (startup and periodic)

Recovery discovers owner-fenced candidates, performs generation-fenced runtime
cleanup for every expired candidate, and only then runs the store repair helper —
before any reaping. The repair transaction atomically fails
`unknown_after_ack_started` turns, requeues recoverable leased turns, fails the
generation, moves allocation/resource rows to `reclaimable`, deletes stale active
contexts, clears checkpoint/restore metadata, and appends durable recovery events.
The repaired session becomes `running_idle` when no work remains, or stays
input-blocking when queued fallback work remains. No path may leave
`status IN ('running_active','checkpointing','checkpointed')` pointing at a
failed/reclaimable generation with no live retry path. `ReapResources` must never
observe recovery-produced reclaimable rows before the cleanup-confirmed repair
commits.

### Checkpoint-image retirement

A `checkpointed` session untouched past `checkpoint_image_retention` releases its
checkpoint image and `/30` slot without destroying the session. Eligibility uses
`retirement_anchor = COALESCE(last_activity_at, checkpoint_created_at, updated_at,
created_at)`. `RetireExpiredCheckpoints` is owner-fenced and, in one transaction:
CASes the session to a non-checkpointed input-acceptable status (clearing
`checkpoint_path` and restore metadata), CASes the generation to `failed` with
`error_class = 'checkpoint_retired'`, moves network/resource rows
`reserved_checkpointed → reclaimable`, and appends the durable status event.
Checkpoint-retired generations are immediately destroyable (they bypass
`failed_retention`); the next message cold-starts, never restores. A stale
restore-CAS that loses the race to retirement is a retryable refetch, not a 500.

`checkpoint_image_retention` is a `time.ParseDuration` value (default `720h`); `0s`
means immediate retirement, negatives fail validation. Use hours (`720h`) — the
parser rejects `d`.

### Recursive-delete safety

Cleanup paths come from DB-backed generation details, so every `os.RemoveAll` target
is validated before deletion: it must resolve under the configured root for that
resource and contain the `gen-<generation_id>` component. Allowed targets are the
per-generation `control` / `runtime` / `bridge` / `logs` / `checkpoint` dirs under
`run_dir`, plus the legacy `CheckpointsRoot/<session_id>` image for migrated rows
only. Missing paths are idempotent success; empty paths, `..` escapes, symlink
escapes, and mismatched generation/session components are hard errors that block
`MarkGenerationResourcesDestroyed`. Filesystem cleanup always runs (independent of
sandbox networking); only nft/veth/netns cleanup is gated on `runsc_network == sandbox`.

## `max_sessions` is a session ceiling, not a live-resource ceiling

`session_retention: 0s` stops automatic destruction of session rows, so `max_sessions`
bounds **non-terminal sessions** (`created`, `running_active`, `running_idle`,
`checkpointing`, `checkpointed`), not live sandboxes. A deployment can hit the ceiling
even after all runtime resources are retired. Config validation allows `max_sessions`
to exceed `/30` pool capacity; pool exhaustion is an allocation-time retryable error.
`/api/quota` reports the session ceiling and the `/30` pool ceiling separately without
clamping one to the other.

`DELETE /api/sessions/{id}` is an immediate close: generation-fenced teardown of the
active generation, then one transaction that cancels pending turns, marks the session
`destroyed`, fails non-terminal generations with `session_destroyed`, reclaims
allocation/resource rows, clears active contexts and checkpoint/restore metadata, and
appends the terminal event — followed by the same resource cleanup path checkpoint
retirement uses, so `/30` slots, network, checkpoint images, and per-generation dirs
are freed immediately. Close preserves messages, artifacts, workspace, and agent-home;
closed sessions no longer count toward `max_sessions` but are not input-acceptable
again (no resume UX yet).

## Out of scope (follow-up)

- A "resume closed/failed session" UX (`POST /api/sessions/{id}/resume`). The data is
  recoverable; the API gates on `sessionstate.CanAcceptInput`.
- Workspace artifact GC — workspaces grow unbounded; disk pressure is bounded by
  deployment scale for now.
- A first-class `archived` status distinct from `destroyed`.
