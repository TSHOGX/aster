# Control Plane (Orchestrator)

> Go service in `orchestrator/` (module `harness-platform/orchestrator`, Go 1.22).
> Owns durable state, generation lifecycle, the bridge, proxy correlation, and
> events. HTTP surface is documented separately in [api.md](./api.md); the sandbox
> execution side in [sandbox-runtime.md](./sandbox-runtime.md).

## Dependencies

Pure-Go SQLite (`modernc.org/sqlite`), `gorilla/websocket`, `fsnotify`,
`google/uuid`, `yaml.v3`. No cgo. The runtime shells out to `runsc`, `ip`, `nft`,
and `curl` for isolation and probing.

## Startup (`cmd/orchestrator/main.go`, `startup.go`)

1. `slog` text logger (`HARNESS_LOG_LEVEL=debug` raises level); ignore SIGPIPE;
   bind SIGINT/SIGTERM to a cancelable context.
2. `config.Load()` then `config.ValidateIsolationRoots(...)` — fatal on error.
3. `store.AcquireOwnerLock(run_dir)` — flock on `<run_dir>/orchestrator.pid`
   guarantees a single orchestrator per data root.
4. `store.OpenWithOptions(...)` opens SQLite (WAL, `foreign_keys=ON`,
   `busy_timeout=5000`, `MaxOpenConns=1`); `WriteOwner` records the owner row.
5. `runStartupRuntimeRecovery`: if `SessionRetention == 0`, clear active-session
   expiry; construct runtime + event hub + artifact watcher + server; then
   `RecoverExpiredRuntimeResources → ReapResources → DestroyReclaimableGenerationResources`.
6. `AlignAutoCheckpointEnabled` (reconcile per-session flag with config);
   `StartOwnerHeartbeat` (5s); `EnsureUser("lab")`.
7. Goroutines: owner-heartbeat-failure watcher (calls stop on failure),
   `watcher.Run` (artifact fsnotify), `MonitorIdleSessions`, `RunMaintenance`.
8. Two servers: the main HTTP server on `cfg.Addr` (`:8090`), and the
   proxy-correlation server on a Unix socket
   (`<run_dir>/proxy-internal/proxy-correlation.sock`).
9. Graceful shutdown (10s) on ctx cancel; removes the proxy socket.

## SQLite schema (`internal/store/schema.go`)

All tables are created idempotently in `ensureSchema`. Summary:

| Table | Purpose |
|---|---|
| `users` | user rows (the `lab` user is always ensured) |
| `auth_sessions` | server-side login sessions: `token_hash` PK, user, created/expires/revoked/last_seen |
| `sessions` | session row: status, `driver_id` (pi/sh), `mode` (agent/shell), workspace, `restore_id`, timestamps, `auto_checkpoint_enabled`, `active_generation_id`, `agent_home_path`, failure_reason/error_class, `title`/`title_custom` (display name + whether the user set it) |
| `messages` | append-only user + assistant chat log (cascade on session delete) |
| `artifacts` | `(session_id, path)` workspace file metadata: size, mod_time |
| `orchestrator_owner` | singleton owner: uuid, boot_id, run dir, heartbeat |
| `egress_policies` | policy digest, allowed egress rules, Doris hosts/ports, dns_policy |
| `agent_runtime_profiles` | dedup'd driver runtime profile: model, output format, sandbox uid/gid, model-access flag, proxy base URL, secret ids |
| `runtime_generations` | generation lifecycle + checkpoint-metadata digests + runsc pin + lease; one non-terminal generation per session (partial unique index) |
| `network_profiles` | per-generation netns/veth/CIDR/gateway/egress; uniqueness of each network resource among non-destroyed rows |
| `runtime_generation_resources` | rendered artifact paths + digests (control dir/manifest, bundle, spec, checkpoint, bridge, log, network hosts), runsc container/pid/pin |
| `turns` | turn queue: sequence, content, status, generation, lease, `claim_request_id`, attempt, retry policy, error, plus the durable activity projection (`phase`, `phase_at`, `phase_tool`, `phase_detail`) |
| `events` | durable event log with dedupe keys (`output_sequence`, `dedupe_key`, one started/finished per `proxy_request_id`) |
| `active_model_request_contexts` | `sandbox_source_ip` → session/generation/turn authorization window for the proxy |
| `sandbox_contracts` | `sandbox-isolation-v1`, schema 2, gate `driver_manifest_v1`: canonical payload + digest |
| `sandbox_contract_artifacts` | per-contract artifact digests (network hosts, control manifest, oci spec, bundle, checkpoint metadata) |
| `sandbox_contract_input_evidence` | runtime-config digest+preimage and agent-manifest digest+payload |
| `session_workspaces` | per-session `/workspace` volume: host path, sandbox uid/gid, provisioning marker + digest |
| `session_driver_homes` | per-(session, driver) `/agent-home` volume, same shape |
| `session_driver_states` | resumable per-driver state: `state_payload`, `state_digest` (`sha256:`), `state_version` |
| `runtime_resource_instances` | authoritative physical-resource record + state machine + identity/absence/post-start evidence digests |

Store code is split by concern: `controlplane.go` (turn claim/ack/complete,
hello-ack, event append, generation lease/heartbeat, fail-generation),
`resources.go` (allocation, `/30` slot allocation, checkpoint claim/restore,
reaper, recovery), `runtime_resource_instances.go`, `proxy.go`,
`data_volumes.go`, `sandbox_contract.go`, `driver_state.go` / `driver_state_pi.go`,
`eventlog.go`, `owner.go`, `auth_sessions.go`.

## State machines

**Session** (`sessionstate/state.go`): `created → running_active → running_idle`,
with `checkpointing → checkpointed → (restore) → running_idle`, and terminal
`failed` / `destroyed`. `CanAcceptInput` = {created, running_idle, checkpointed}.

**Generation** (`runtime_generations.status`):
`allocating → starting → probing → active → idle → checkpointing → checkpointed →
restoring → failed → destroyed`. One non-terminal generation per session.

**Runtime resource instance** (`runtime_resource_instances.state`):
`allocated → materializing → ready → live → checkpoint_reserved → retiring →
reconciling → absent_verified → destroyed`, tracked with worker leases and
identity/absence evidence digests.

**Turn** (`turns.status`): `queued → leased → running → completed | failed | canceled`.

`turns` is also the single source of truth for what the UI shows as the agent's
current activity. `phase` is written on coarse boundaries (turn start, tool
start/end, first text of a block, compaction, retry) by `SetTurnPhase` — never per
token — and `sessions.turn_revision` is bumped by **triggers** on `turns` for
every insert, status change, phase change, or delete. The triggers rather than the
call sites own that invariant because `turns` is written from a dozen places,
several of them recovery and reaper paths; a writer that forgot to bump would leave
clients holding a stale projection, a bug that surfaces only as confusing UI with
nothing failing loudly. See
[proposals/session-runtime-projection.md](./proposals/session-runtime-projection.md).

## Turn / generation flow

`POST /api/sessions/{id}/messages` (`server.go: sendMessage`):

1. Sweep expired sessions; check `CanAcceptInput`.
2. `ensureActiveGeneration`: reuse the live generation, restore a `checkpointed`
   one, or allocate a new one.
3. `startEnsuredGeneration`: prepare artifacts → store sandbox contract + input
   evidence → create the runtime resource instance → materialize/ready → mark
   generation `starting` → launch `runsc` → bridge startup probe → mark
   resource + generation `live`. Guarded by a renewing **generation start lease**
   (`startLeaseKeeper`); a lost lease aborts and tears down.
4. `EnqueueTurnMessage`: insert a `queued` turn + message; session → `running_active`.
5. The sandbox bridge claims and runs the turn; `CompleteTurn` records driver state
   and (if no in-flight turns remain) marks the generation/session idle.

Cold-fallback sessions (checkpointed with queued turns) are restarted by
`startColdFallbackSessions` during maintenance. Retryable start failures retire only
the failed generation and keep the session retryable — never terminal — per
[decisions/session-lifetime.md](./decisions/session-lifetime.md).

**Parked turns are invisible to all of this.** A turn carries `auto_dispatch`; Stop
clears it on everything the user had queued (`turn_park.go`). `ClaimNextTurn` and
`ListColdFallbackSessions` both require `auto_dispatch = 1`, so a parked turn is
never granted to a sandbox and never pulls one back up *on its own* — it waits for
the user to release it with `POST .../turns/{turnID}/send`. Without that predicate,
a queue parked by Stop would burn a cold start for a prompt the user may never
release.

The corollary is that a parked queue does not hold a sandbox open either: it is
excluded from the pending-turn predicate that `ListAutoCheckpointCandidates` and
`ListIdleReleaseCandidates` use, so compute can be reclaimed under rows that are
still on screen. Releasing therefore goes through the same
`ensureRunnableGenerationHTTP` a new prompt does, cold-starting a `checkpointed`
session before flipping `auto_dispatch` back on — the wake is paid when the user
asks for it, not to keep the queue warm.

## Bridge (`internal/bridge/`)

The in-sandbox client and the host processor communicate over a file queue on the
bridge bind mount, never a socket. This survives sandbox restarts and host crashes.

**Layout** (`filequeue.go: EnsureLayout`), under the bridge root:
`inbox`, `outbox`, `heartbeat`, `host-heartbeat`, `tmp`, `host-tmp`, plus a sibling
host-owned control root (`inbox`, `host-heartbeat`, `host-tmp`). Sandbox-writable
dirs are group-owned; host-owned dirs are root-owned; the inbox is mounted read-only
into the sandbox.

**Durability**: each message is an `Envelope` JSON file named `%020d.json`
(monotonic sequence). `Write` writes to a tmp file, then `os.Link` into the queue
and fsyncs the directory — atomic and crash-safe. Inbox writes prune prior sequence
files (single-slot request/response). Heartbeat and `checkpoint-ready` are
atomic-rename timestamp files.

**Message types** (`harness_bridge_v2`, version 2, turn schema `RunTurn`):
`hello`/`hello_ack`, `heartbeat`, `probe_network`, `claim_next_turn`, `resume_turn`,
`grant`, `no_work`, `ack_turn_started`, `emit_output`, `ack_turn_completed`, `error`.

**Turn control** (`turncontrol.go`) is the one host→sandbox path that is *not* a
bridge message, because it has to reach a turn that is already executing. The
sandbox is the only requester in `harness_bridge_v2`, and during a turn its bridge
client is blocked reading the driver — so nothing is polling the inbox and no
response can be granted. Instead the host rewrites a single `turn-control.json` in
the host-owned inbox (atomic rename, versioned, append-only log of commands), and a
watcher thread inside the sandbox applies each one to the running turn:

- `abort` — what Stop delivers. The turn ends with `stopReason: "aborted"`; the
  driver process, its session history and the sandbox all survive.
- `steer` — what ↑ delivers while a turn runs. The text is injected into the
  running turn, so work already done is not discarded.

Each entry carries a **token** (applied at most once — the sandbox cannot delete a
file in a read-only mount) and a **turn_id** (a leftover entry must never reach a
later turn). The filename is deliberately not a 20-digit sequence name: both queue
readers accept only that form and the response prune only deletes it, so the control
file is inert to the queue machinery — no new mount, no protocol bump.

**Processor** (`processor.go: ProcessOnce → handle`):

- `hello` → validate (protocol v2, driver_id matches the generation's manifest
  driver, turn schema) → `hello_ack` with last-output-sequence-per-turn + any leased turn.
- `heartbeat` → renew generation heartbeat.
- `probe_network` → requires hello; runs the optional probe handler → `no_work {probe_ok}`.
- `claim_next_turn` → requires hello + probed → `grant` (with driver-state payload) or `no_work`.
- `resume_turn` → replay a leased turn → `grant` marked replayed.
- `ack_turn_started` → records `sandbox_source_ip`, opens the model-proxy
  authorization window → durable event.
- `emit_output` → validates positive `output_sequence` (and native-event schema
  when present) → append event; a duplicate-sequence *mismatch* fails the generation.
  The turn CAS (`assertOutputEventTurnTx`) distinguishes two failures, because they
  need opposite handling and used to be one indistinguishable error:
  - the turn row is **already terminal** → permanent. The generation is emitting
    for a turn the host has retired, so the event is discarded and the envelope
    unlinked (`IsOutputEventTurnNotRunning`). It is *not* a generation fault: a
    late event must not cost the user their sandbox.
  - the **lease/generation** predicates did not match → transient. The error is
    propagated so the envelope is retained and retried.

  Conflating them meant `ProcessOnce` returned before unlinking and re-read the
  same record every poll — one generation logged 2277 `output event turn CAS
  failed` in two minutes, and because the pass never completed, no later envelope
  (including the runtime projection the client waits on) was processed. That is
  what left the UI on "Agent is thinking…". The batch path
  (`handleOutputBatch`) already recorded per-envelope failures and continued; the
  single-envelope path now matches it.
- Any generation failure (including a bridge protocol mismatch) uses the atomic
  `FailGenerationWithEvent` path: turn/generation/resource rows, the retryable
  session status, and a `generation.error` event commit together. The server
  publishes the returned event id, so a process crash cannot leave
  `sessions.status=running_active` pointing at a failed generation.
- `ack_turn_completed` → `CompleteTurn` with `driver_state_update`; permanent errors
  fail the generation, transient ones are retried (envelope retained).

`RunMaintenance` first reconciles historical state drift (terminal/missing active
generations), then drives the processor each poll interval: it lists bridge-poll
generations, `MarkReady`s them, and runs `ProcessOnce` with an `AfterCommit` hook
that publishes the durable event and routes `emit_output` to the per-turn
stream parser and `ack_turn_completed` to a parser flush + artifact scan. Before a
generation is marked live, `waitForBridgeStartupReadiness` observes heartbeat +
hello + probe_network directly.

## Events (`internal/events/hub.go`, `server/stream_parser.go`)

The hub is in-process pub/sub. `Subscribe(sessionID)` returns a buffered channel;
the empty-string key is the global feed. `Publish` fans out to both keys
**non-blocking** (drops on a full channel) — durable delivery is guaranteed by the
`events` table, not the hub. Durable events are written first (with dedupe keys),
then re-published.

`stream_parser.go` normalizes raw driver output per driver
(`pi` / `shell` / `native_events_probe`) into `agent.delta`,
`agent.message`, `agent.output`, `system.status`, and persists assistant messages.

SSE delivery (`GET /api/events/stream`) supports `Last-Event-ID` / `last_event_id`
cursor replay from the durable log, emits a `replay_gap` event when the retention
window is exceeded, sends a 20s heartbeat, and filters per-event by session ownership.

## Auth (`internal/auth/`, `store/auth_sessions.go`)

- **PBKDF2-HMAC-SHA256**, standard-library only, `600_000` iterations, 16-byte salt,
  32-byte key, encoded `pbkdf2-sha256$<iter>$<salt-b64>$<hash-b64>`. Verified with
  constant-time compare. Hashes are generated by `cmd/authhash`.
- **Users** load from `config/auth-users.yaml` (`{users:[{username, password_hash}]}`);
  username regex `^[A-Za-z0-9_-]{1,64}$`. A missing file means open mode.
- **Hot reload** (`internal/server/auth_registry.go`): the file is re-read when its
  mtime/size changes, so `orchestrator user add|passwd|rm` takes effect on the next
  login with no restart. The stat is throttled to 1/s on the per-request path and
  forced on login. Reload is one-directional: a read/parse failure, or the file
  disappearing, keeps the last good credential set and logs an error — an empty set
  would mean "no login required", so a broken file must never open the gate.
- **Login**: open mode issues a `lab` session directly; otherwise rate-limit by
  `username|clientIP`, verify against the pbkdf2 map (with a dummy verify for unknown
  users to equalize timing) or `HARNESS_LAB_PASSWORD`.
- **Session cookie**: 32 random bytes; only the SHA-256 hex is stored
  (`CreateAuthSession`, 24h TTL). Cookie is HttpOnly, SameSite=Strict, `Secure` +
  `__Host-` prefix over HTTPS (honoring trusted `X-Forwarded-Proto`), with HSTS.
- **Rate limit** (`ratelimit.go`): 5 failures / 15 min locks the key.
- **Owner scoping**: `authorizeSession` returns 404 (not 403) on missing session or
  owner mismatch, so sessions can't be probed; event streams and artifact downloads
  re-check ownership (30s owner cache).

## Proxy correlation (`store/proxy.go`, `server` Unix socket)

The model proxy calls `POST /internal/proxy/requests/{start,finish}` over the Unix
socket, authenticated by SO_PEERCRED against `harness.proxy_service_identity`.
`StartProxyRequest` validates that the source IP maps to a live
`active_model_request_contexts` row (opened at `ack_turn_started`) whose contract
allows model access, then returns session/turn/generation/sequence. This is how a
sandbox gets model access only during its own active turn. See [security.md](./security.md).

## Maintenance, reaper, recovery

`RunMaintenance` (periodic) and startup recovery share the same cleanup-confirmed
repair path: discover owner-fenced expired candidates, perform generation-fenced
runtime teardown (delete/prove-absence of the runsc container + evict the in-memory
entry), then atomically repair turns/generation/resources/session and append durable
recovery events. Only then may `ReapResources` /
`DestroyReclaimableGenerationResources` physically delete reclaimable rows.
`failed_retention` and `checkpoint_image_retention` gate physical cleanup timing.
The full contract (including checkpoint retirement and legacy-path safety) is in
[decisions/session-lifetime.md](./decisions/session-lifetime.md).

## Artifacts (`internal/artifacts/watcher.go`)

fsnotify recursively watches verified session workspace roots (refreshed every 1s),
emitting `artifact.updated` on create/write and `artifact.deleted` on remove/rename,
rejecting symlinks and workspace escapes. `ScanSession` does a full walk after each
turn completion so metadata converges even if an fsnotify event was missed.
