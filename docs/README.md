# Documentation Index

Documentation for the **Agent Runtime Platform** — a Go control plane that runs each
AI agent session in its own gVisor (`runsc`) sandbox, driven by a Next.js workbench.

These docs were rewritten 2026-07-06 from the current `publish/` codebase and are the
source of truth for how the system works today. Start with
[architecture.md](./architecture.md).

## Core documentation

| Doc | Covers |
|---|---|
| [architecture.md](./architecture.md) | System overview, components, session/turn model, boundary — **start here** |
| [control-plane.md](./control-plane.md) | Orchestrator internals: startup, SQLite schema, state machines, bridge, events, auth, reaper |
| [sandbox-runtime.md](./sandbox-runtime.md) | runsc launch, OCI spec, mount plan, network/egress, idle release, checkpoint |
| [sandbox-image.md](./sandbox-image.md) | rootfs build, image manifest, entrypoint, bridge client, shell agent, skills |
| [drivers.md](./drivers.md) | Pi / Claude Code / shell drivers, product-mode resolution, manifest validation |
| [api.md](./api.md) | HTTP routes, DTOs, event types, proxy-correlation routes |
| [frontend.md](./frontend.md) | Workbench architecture, same-origin proxy, event handling, auth flow |
| [configuration.md](./configuration.md) | `harness.yaml` reference, `HARNESS_*` env vars, ports, data roots |
| [security.md](./security.md) | Threat model, isolation, host-only credentials, proxy correlation, auth |
| [verification.md](./verification.md) | How to verify a change: test commands, live smoke, platform notes |
| [operations.md](./operations.md) | Publish deployment & operations runbook (中文；systemd/nginx/proxy) |
| [parallel-development.md](./parallel-development.md) | Parallel dev on `publish` via `git worktree` (中文) |

## Decisions & roadmap (`decisions/`)

Preserved architecture decision records and forward-looking plans:

| Doc | Covers |
|---|---|
| [decisions/gvisor-vs-firecracker.md](./decisions/gvisor-vs-firecracker.md) | Why gVisor was chosen over Firecracker (ADR) |
| [decisions/session-lifetime.md](./decisions/session-lifetime.md) | Session vs runtime lifetime separation (durable invariant) |
| [decisions/runsc-checkpoint-research.md](./decisions/runsc-checkpoint-research.md) | runsc checkpoint/restore capability; no warm sentry |
| [decisions/trajectory-pipeline.md](./decisions/trajectory-pipeline.md) | Future trajectory → memory → skill pipeline (not implemented) |

---

The `docs/` tree now contains only current-truth documentation and the decision
records above. The phase-era docs (`PLAN.md`, `phaseN/`, status logs) and the
`tools/release/` qualification framework were removed; verification guidance is in
[verification.md](./verification.md).
