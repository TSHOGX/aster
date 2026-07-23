# Agent Runtime Platform — Architecture

> Single source of truth for how the system works today. Deep-dive documents are
> linked per section; start here.

## What this is

The Agent Runtime Platform runs each long-lived AI agent session inside its own
gVisor (`runsc`) sandbox, managed by a Go control plane and driven by a Next.js
workbench in the browser.

- **Control plane** (`orchestrator/`): owns durable session/turn state, allocates
  and reconciles per-generation sandbox runtimes, routes turns through a file-queue
  bridge, correlates model-proxy requests, records artifact metadata, and streams
  events to the browser.
- **Sandbox runtime** (`sandbox-image/` + `orchestrator/internal/runtime/`):
  launches `runsc` directly with a read-only rootfs, an exact mount plan, a
  per-generation network namespace, and the selected agent driver.
- **Workbench** (`frontend/`): a Next.js single-page app that proxies same-origin
  to the orchestrator, drives sessions, and renders live output + an artifact tree.

Two agent drivers ship: **Pi** (the default product Agent) and a **PTY shell**.
A deployment selects which driver each product mode resolves to.

## Component diagram

```text
Browser
  │  HTTPS (same-origin)
  ▼
Next.js workbench (frontend/)
  │  server-side route handlers proxy /api/* and /artifacts/*
  ▼
Go orchestrator (orchestrator/)  ──────────────────────────────────┐
  ├─ HTTP API + auth (:8090)                                        │
  ├─ SQLite store (WAL, single writer)                              │
  ├─ durable event hub  ── SSE /api/events/stream ──► browser       │
  ├─ artifact watcher (fsnotify over /workspace)                    │
  ├─ proxy-correlation server (Unix socket, SO_PEERCRED)  ◄── model proxy
  └─ runtime manager
        ├─ generation / resource reconciler + reaper
        ├─ bridge processor (file-queue claim/ack)
        └─ runsc run / restore / checkpoint / delete
              │
              ▼
        gVisor sandbox (per generation)
          ├─ harness-agent-entrypoint (PID 1 shim)
          ├─ harness-bridge-client  ── file queue ─► control plane
          ├─ pi | harness-shell-agent
          ├─ /workspace   (exact rw bind)
          ├─ /agent-home  (exact rw bind)
          └─ /harness-control (ro) + bridge (rw)
```

The model proxy is a **separate process** (not in this repo; deployed as
`claude-code-proxy`, the historical service name). It translates the sandbox's
Anthropic-style requests to the upstream provider and calls back into the
orchestrator's Unix socket to authorize each request against live turn context.
See [security.md](./security.md) and [operations.md](./operations.md).

## Runtime vs control plane

**Runtime** is the execution substrate: `runsc`, the sandbox process, rootfs,
mounts, network namespace, bridge client, the selected driver, and
checkpoint/restore mechanics. Its concerns are isolation, resource shape, and
recovery. See [sandbox-runtime.md](./sandbox-runtime.md).

**Control plane** is the host-side manager: session and turn state, generation
allocation, resource reconciliation, control-manifest rendering, bridge
claim/ack processing, event persistence, artifact metadata, proxy correlation,
quota, and retention. Its concerns are correctness, durable state, and
observability. See [control-plane.md](./control-plane.md).

The invariant that separates the two — sessions/history/workspace persist
independently of live runtime resources — is a durable contract documented in
[decisions/session-lifetime.md](./decisions/session-lifetime.md).

## Session lifecycle

Canonical session statuses (`orchestrator/internal/sessionstate/state.go`):

```text
created  running_active  running_idle  checkpointing  checkpointed  failed  destroyed
```

- **Accepts input**: `created`, `running_idle`, `checkpointed`.
- **Busy**: `running_active`, `checkpointing`.
- **Terminal**: `failed`, `destroyed`.

A **generation** is one sandbox attempt for a session. At most one non-terminal
generation exists per session. `POST /api/sessions/{id}/messages` ensures an active
generation (reuse the live one, cold-restore a `checkpointed` one, or cold-start a
new one), enqueues the turn, and flips the session to `running_active`. The
sandbox's bridge then claims the turn, acks start, streams output, and acks
completion; the session returns to `running_idle`.

`DELETE /api/sessions/{id}` moves the session to `destroyed` and reclaims runtime
resources while preserving messages, artifacts, workspace, and agent-home.

Each session carries a human-readable `title` for the sidebar (falling back to the
raw id when unset). The first message seeds a placeholder title and, when a title
LLM is configured, an async call refines it; `PATCH /api/sessions/{id}` lets the
user rename it, after which automatic naming leaves it alone.

## Turn execution via the bridge

Turns are stored in SQLite before execution. The in-sandbox `harness-bridge-client`
talks to the control plane over a **crash-safe file queue** on a shared bind mount
(`/harness-control/bridge`) — never a socket. Protocol `harness_bridge_v2`:

```text
hello → hello_ack        (identity, resume boundary)
heartbeat                (liveness)
probe_network            (proxy reachability)
claim_next_turn → grant  (lease a queued turn + driver state)
ack_turn_started         (opens the model-proxy authorization window)
emit_output …            (streamed, sequence-numbered)
ack_turn_completed       (durable turn outcome + driver-state update)
```

Full message and state-machine detail: [control-plane.md](./control-plane.md).

## Drivers and product modes

The browser only ever picks a **product mode** (`agent` or `shell`), never a raw
driver id. `GET /api/deployment-capabilities` reports which modes are creatable.
Mode → driver resolution (`orchestrator/internal/server/deployment.go`):

- `agent` → `harness.default_agent` (checked-in: **pi**), must be an agent-kind driver.
- `shell` → `sh`, a shell-kind driver.

Resolution succeeds only if the driver is enabled in `harness.agents`, its runtime
provider supports the driver's capabilities, a model profile is enabled for
model-access drivers, and the driver is present in the rootfs image manifest
(`/etc/harness-image/agents.json`) with matching, digest-verified facts. Details:
[drivers.md](./drivers.md).

## Events and output

Runtime stdout/stderr is fanned out per container through an `OutputHub` (blocking
backpressure — output is part of the turn protocol). The bridge's `emit_output` is
parsed per driver (`stream_parser.go`) into normalized events, persisted to the
durable `events` table, then published through the event hub (non-blocking fan-out).

Notable event types: `session.created` / `session.<status>`, `session.updated`,
`generation.error`, `agent.delta`, `agent.message`, `agent.output`, `system.status`,
`artifact.updated`, `artifact.deleted`, `notification.created`, plus
proxy-correlation events. Two delivery endpoints: `GET /api/events/stream` (SSE, with
`Last-Event-ID` replay) and `GET /api/events` (WebSocket compatibility). Full list:
[api.md](./api.md).

## Sandbox boundary and security

The boundary is `sandbox-isolation-v1` with a schema-v2 sandbox contract per
generation. Key properties:

- Read-only rootfs; writable surfaces are only explicit binds (`/workspace`,
  `/agent-home`, bridge) or scratch tmpfs.
- Exact `/workspace` and `/agent-home` binds; parent `/sessions` / `/agent-homes`
  mounts are forbidden; `/harness-secrets` is absent.
- Non-root agent (uid/gid `65534`), empty capabilities, `noNewPrivileges`.
- Per-generation network namespace with a configurable egress policy. The
  checked-in default is **open egress** (`network.egress.allow_all: true`): the
  sandbox can reach the whole public internet (pip, GitHub/PyPI, web search).
  Flipping `allow_all` off restores a default-drop policy where the only allowed
  egress is Doris (`172.16.0.138:9030`) and the model-proxy alias. Egress is baked
  per generation at sandbox creation.
- **Model credentials never enter the sandbox**, independent of the egress policy.
  The sandbox reaches the model
  proxy via a hostname alias; the proxy authorizes each request over an
  SO_PEERCRED-authenticated Unix socket against live turn context. Secret mounts
  are rejected at generation validation.
- Public API/event payloads are scrubbed of internal ids and host paths
  (`event_dto.go`, `session_dto.go`).

Full detail: [security.md](./security.md).

## Persistence and configuration

State lives in a single SQLite database (WAL, one writer). ~20 tables cover
sessions, messages, turns, durable events, proxy request context, runtime
generations, runtime resource instances, network profiles, sandbox contracts and
input evidence, data volumes, driver state, and artifact metadata. See the table
reference in [control-plane.md](./control-plane.md).

Primary config is `config/harness.yaml` under a strict `harness:` schema (unknown
keys fail startup), plus `HARNESS_*` env overrides. Full reference:
[configuration.md](./configuration.md). Checked-in defaults: orchestrator `:8090`,
model proxy `:8083`, CIDR pool `10.201.0.0/16`, `default_agent: pi`,
`session_retention: 0s`, `max_sessions: 30`, open egress (`allow_all: true`),
automatic idle release enabled after 2h.

## Checkpoint / idle release

`harness.checkpoint.auto_enabled: true` enables automatic **idle release**: after
`idle_threshold` (2h), the monitor tears down an idle sandbox to free the compute
slot — it does **not** take a `runsc` checkpoint image. The session moves to
`checkpointed` (logical "released, cold-restore later"), and the next message
cold-starts a fresh generation from the retained workspace + agent-home. True
`runsc` checkpoint/restore also exists (gated by strict metadata-digest matching)
but is separate from idle release. See [sandbox-runtime.md](./sandbox-runtime.md)
for mechanics and
[decisions/runsc-checkpoint-research.md](./decisions/runsc-checkpoint-research.md)
for why there is no warm-sentry fast path.

## Source map

| Area | Path | Doc |
|---|---|---|
| HTTP API, auth, turn orchestration | `orchestrator/internal/server/` | [api.md](./api.md), [control-plane.md](./control-plane.md) |
| runsc rendering & lifecycle | `orchestrator/internal/runtime/` | [sandbox-runtime.md](./sandbox-runtime.md) |
| bridge claim/ack | `orchestrator/internal/bridge/` | [control-plane.md](./control-plane.md) |
| SQLite schema & mutations | `orchestrator/internal/store/` | [control-plane.md](./control-plane.md) |
| drivers / provider registry | `orchestrator/internal/agents/` | [drivers.md](./drivers.md) |
| artifact scanning | `orchestrator/internal/artifacts/` | [control-plane.md](./control-plane.md) |
| rootfs, entrypoint, bridge client, skills | `sandbox-image/` | [sandbox-image.md](./sandbox-image.md) |
| workbench + proxy routes | `frontend/` | [frontend.md](./frontend.md) |
| verification / tests | `*/testdata/`, `*_test.go`, `sandbox-image/tests/` | [verification.md](./verification.md) |
| checked-in config | `config/harness.yaml` | [configuration.md](./configuration.md) |

## Known limitations

- Supported drivers are Pi and the PTY shell. New adapters must enter
  through the driver/provider contract in [drivers.md](./drivers.md).
- The artifact UI is mostly read-only: users can upload files and edit small text
  files in place, but all rich generation is done by the agent/shell.
- The event hub is for UI streaming, not an audit log; the durable `events` table is.
- Single orchestrator per data root (flock + DB owner heartbeat). Multi-orchestrator
  HA, tenant authz, credential rotation, and resource limits are future work.
