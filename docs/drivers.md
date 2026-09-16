# Drivers and Product Modes

> How the platform exposes agents to users, and how a product mode resolves to a
> concrete driver. Registry in `orchestrator/internal/agents/agents.go`; resolution
> in `orchestrator/internal/server/deployment.go`; in-sandbox runners in
> `sandbox-image/files/usr/local/bin/harness-bridge-client`.

## Product modes vs drivers

Users never pick a driver id. The workbench offers **product modes** and calls
`GET /api/deployment-capabilities`, which reports each mode's visibility, whether it
is creatable, and a disabled reason code if not.

| Product mode | Resolves to | Kind |
|---|---|---|
| `agent` | `harness.default_agent` (checked-in: **pi**) | must be agent-kind |
| `shell` | `sh` | must be shell-kind |

Pi is the only agent-kind driver that ships today.

`resolveModeDeployment("agent")` uses `cfg.DefaultAgent`; `"shell"` always uses
`sh`. Raw `agent` input to `POST /api/sessions` is rejected — only `{"mode": ...}`.

## The two drivers

| Driver id | Label | Kind | Output schema | Model access | Interrupt | Compaction | Binary |
|---|---|---|---|---|---|---|---|
| `pi` | Pi | agent | `pi_rpc_events_v1.0` | yes | yes | no | `/usr/local/bin/pi` |
| `sh` | Shell | shell | `shell_pty_v1` | **no** | yes | no | `/usr/local/bin/harness-shell-agent` |

Both use bridge protocol `harness_bridge_v2` (version 2) and turn input schema
`RunTurn`, and require the same runtime capabilities (`exec_stream`, `filesystem_rw`,
`kill`, `logs`, `network_policy`, `snapshot_disk`, `stdin`).

### Pi (default Agent)

- Package `@earendil-works/pi-coding-agent@0.77.0`, with a **pinned shasum and
  sha512 integrity** verified against the installed binary.
- Runs as a long-lived **JSON-RPC process** (`pi --mode rpc --provider
  harness_anthropic_proxy --model sonnet --session-dir ...`), driven over
  stdin/stdout by the bridge client's `PiRPCTurnRunner`.
- **Config materialization**: read-only `models.json` and `settings.json` are
  projected into `/agent-home/.pi/agent/` (exact `ro` binds); the runner rejects
  them if they are symlinks or writable by the sandbox uid/gid.
- **Runtime layout**: env `PI_CODING_AGENT_DIR=/agent-home/.pi/agent`,
  `PI_CODING_AGENT_SESSION_DIR=.../sessions`, `PI_OFFLINE=1`,
  `PI_SKIP_VERSION_CHECK=1`, `PI_TELEMETRY=0`; home dirs `.pi/agent/sessions`.
- **System prompt**: injected via `--append-system-prompt` from
  `/usr/local/share/harness/pi-system-prompt.md` (generic identity and knowledge
  read-only rules). See [sandbox-image.md](./sandbox-image.md).
- **Restore**: driver state is a `pi_session` sidecar digest; when it changes, the
  runner issues `switch_session` (verified via `get_session_stats`) before the
  prompt. Restore state advances only after a successful completed turn.
- **Turn boundary** (`agent_end`, not `turn_end`): pi frames one prompt as

      agent_start … [turn_start … turn_end]* … agent_end

  `turn_end` is an **inner, per-model-turn** boundary that repeats during tool
  use; `agent_end` ends the prompt. `run_turn` decides the turn's status on the
  first non-`toolUse` `turn_end`, so at least one `agent_end` is always still
  unread when it stops — after an `abort`, `response{command:"abort"}` as well.

  The runner therefore holds an invariant: **when `run_turn` returns, pi's stdout
  contains no events belonging to the turn that just finished.** It is
  established in a `finally`, by `_barrier_request` — a `get_session_stats` whose
  own response id acts as the barrier: everything read before it is prior-turn
  residue by definition, and the response proves pi has caught up. On a completed
  turn that same response supplies the driver-state update, so a healthy turn
  costs no extra round trip. The drain is bounded and only logs on timeout: the
  abort path must not be what makes a Stop hang.

  Without that invariant the residue was left in the pipe and the **next** turn
  read it as its own terminal event, acking `completed` in milliseconds while pi
  answered a prompt nobody was listening to. Only turns after an early return
  (abort/failure) were affected, because the completed path's `get_session_stats`
  had been swallowing the stray `agent_end` by accident. See
  [runtime synchronization](./runtime-state-synchronization.md).
- **Interrupt and steer** (`abort` / `steer` RPC): both act on the turn that is
  executing and leave the pi process, its session history and the sandbox intact.
  `abort` ends the turn with `stopReason: "aborted"` — whatever already streamed
  stays in the transcript, and the turn row is recorded `canceled` /
  `user_interrupted`. `steer` does **not** abort: it injects a message into the
  running turn, so work already done is not redone. That distinction is the whole
  API surface below — Stop sends `abort`, and ↑ on a queue row sends `steer` when a
  turn is running.

  The turn thread blocks in `_read_rpc` for the whole turn, so neither can be
  delivered on it. The host writes a JSON control log to the bridge inbox and a
  watcher thread in the bridge client applies it, serialized against the turn
  thread by a stdin lock. See [api.md](./api.md) for the endpoints and
  [sandbox-image.md](./sandbox-image.md) for the file's placement.

### Shell (`sh`)

- PTY-backed `harness-shell-agent`; **no model access** (no proxy, no credentials).
- Enter runs a command, Shift+Enter inserts a newline; a per-command sentinel
  delimits turns and carries the exit code. Interruptible via
  `POST /api/sessions/{id}/interrupt` (sends `Ctrl-C` + SIGINT).
- Emits `harness.shell_output` and `harness.turn_done`.

## Deployment resolution (`resolveDriverDeployment`)

For a mode to be creatable, all of the following must hold:

1. The driver spec exists and matches the mode's kind (agent vs shell).
2. An entry in `harness.agents` is **enabled** for that driver.
3. Its `runtime_provider` is defined and enabled, and supports the driver's required
   capabilities (`EnsureDriverSupportedByProvider`). Only `local_runsc` (gVisor)
   ships.
4. For model-access drivers, the referenced `model_profile` is enabled, has a
   non-empty `model`, and `proxy_ref == model_proxy`.
5. The driver is present in the **agent image manifest** with matching, verified facts.

Failures carry a code surfaced in `/api/deployment-capabilities`:
`default_unavailable`, `disabled`, `provider_unsupported`, `operator_unavailable`,
`missing_from_image`.

## Agent image manifest (`/etc/harness-image/agents.json`)

The rootfs ships a manifest (schema_version 1) listing each installed driver. During
resolution the orchestrator loads it and, per selected driver, verifies:

- `driver_id`, `kind`, `bridge_protocol`, `bridge_protocol_version`,
  `turn_input_schema`, `output_schema`, `model_access` match the registry spec.
- `binary_path` matches the expected path.
- `installed_binary_digest` is present (`sha256:…`) and, for a real (non-synthetic)
  manifest, **matches the actual sha256 of the binary in the rootfs**.
- Package facts (name / version / shasum / integrity / event schema) match the
  registry pins (this is what enforces Pi 0.77.0 exactly).

If no manifest file exists and `RequireAgentManifest` is false, a **synthetic**
manifest is built from the registry (used for tests / manifest-less dev). In the
running service `RequireAgentManifest` is true, so the rootfs must carry a real
manifest — build it with `SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh`
(see [sandbox-image.md](./sandbox-image.md)).

## Secret grants

One secret-grant spec exists: `model_provider` / `anthropic_messages`, exposure
`proxy_only`, max TTL 24h. Model credentials are never mounted into the sandbox;
this grant only describes the proxy-mediated access. See [security.md](./security.md).

## Driver private state

Resumable per-driver state lives in `session_driver_states`
(`state_payload`, `state_digest` = `sha256:…`, `state_version`, updated
generation/turn). `ack_turn_completed` carries a `driver_state_update` that is
applied with CAS fencing and **dropped for any non-completed turn status**, so a
failed turn can't corrupt resume state. Pi state is host-validated
(`driver_state_pi.go`) before it is trusted for restore.
