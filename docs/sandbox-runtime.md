# Sandbox Runtime

> The execution substrate: how the control plane renders and launches a gVisor
> (`runsc`) sandbox for one generation. Code in `orchestrator/internal/runtime/`.
> The image the sandbox boots is documented in [sandbox-image.md](./sandbox-image.md);
> the security properties in [security.md](./security.md).

## Overview

A **generation** is one sandbox attempt for a session. The runtime keeps an
in-process map of live `Container`s keyed by session id (each with stdin/stdout/
stderr pipes and an `OutputHub`) and launches `runsc` directly — there is no
containerd or Docker daemon.

Launch is two-phase: `PrepareGeneration` renders all artifacts and sets up the
network; `Start` launches `runsc`. The control plane's `startEnsuredGeneration`
orchestrates the larger state machine around these (see
[control-plane.md](./control-plane.md)).

## Rendered artifacts (`renderGenerationArtifacts`)

Per generation the runtime writes:

- **OCI runtime spec** (`config.json`) — see below.
- **Control manifest** (`session.json`, wrapped `{payload, digest}`) — the
  digest-verified handshake the entrypoint reads. See
  [sandbox-image.md](./sandbox-image.md) for the reader side.
- **Network `/etc/hosts` projection** — includes the model-proxy alias
  (`harness-model-proxy.internal`) → host gateway IP.
- **Driver-config projections** — e.g. Pi's read-only `models.json` / `settings.json`.

It records digests for the bundle, runtime config, spec, and projected control
manifest, and captures a `runscPin` (platform, version, binary path, binary
sha256) that is re-verified before every launch, checkpoint, and cleanup.

## OCI spec (`renderSandboxIsolatedRuntimeSpec`)

- Rootfs **read-only**; entrypoint `/usr/local/bin/harness-agent-entrypoint`.
- Non-root user from the configured sandbox identity (default uid/gid `65534`).
- `noNewPrivileges: true`, **empty capability sets**.
- Limits: RLIMIT_NOFILE 1024, memory 1 GiB, 1024 CPU shares, PID limit 256.
- Namespaces: pid, ipc, uts, mount, and (in sandbox network mode) network with the
  generation's `netns_path`.
- A large `HARNESS_*` env block: session/generation/profile ids and
  `HARNESS_EXPECTED_*` cross-checks, bridge dir/mode/intervals, driver id, probe
  statuses, plus per-driver env (e.g. Pi's `PI_*`).

## Mount plan (`mount_plan.go`)

`BuildSandboxMountPlan` produces a **strict allow-list** plan; `Validate` rejects
`rbind` (mounts must be exact), duplicate destinations, and forbidden destinations.

**Content mounts:**

| Name | Destination | Mode | Notes |
|---|---|---|---|
| workspace | `/workspace` | rw | exact bind, `nosuid,nodev` |
| agent_home | `/agent-home` | rw | exact bind, `nosuid,nodev` |
| control | `/harness-control` | ro | `nosuid,nodev,noexec` |
| bridge | `/harness-control/bridge` | rw | gVisor `share: exclusive` annotation |
| bridge_inbox | `/harness-control/bridge/inbox` | ro | host-owned |
| bridge_host_tmp | `/harness-control/bridge/host-tmp` | ro | host-owned |
| network_hosts | `/etc/hosts` | ro | per-generation projection |
| driver config | e.g. `/agent-home/.pi/agent/models.json` | ro | from `DriverConfigMaterializationSpecs` |

**Scratch mounts:** tmpfs `/tmp` and `/var/tmp` (rw).

**Forbidden destinations:** parent `/sessions`, `/agent-homes`, and
`/harness-secrets` are rejected — the sandbox can only ever see its own exact
workspace/agent-home binds, and no secret mount.

## Launch (`startFresh`)

```text
runsc -root <RUNSC_ROOT> -platform systrap -overlay2 none -network sandbox \
      run -bundle <dir> <containerID>
```

The runtime pipes stdin/stdout/stderr, spawns two `scanLines` goroutines that
publish output to the `OutputHub`, and a monitor goroutine that waits for exit and
closes the hub. It produces a `RuntimeResourcePostStartProof` (runsc running-state
evidence + netns/veth/nft presence evidence) recorded on the resource instance.

The `OutputHub` (`output_hub.go`) applies **backpressure** — `Publish` blocks on a
full subscriber channel rather than dropping, because streamed output is part of the
turn protocol (contrast with the event hub, which drops on full channels).

## Network (`ensureSandboxNetwork`)

Each generation owns a persisted `/30` from `harness.network.cidr_pool`
(`10.201.0.0/16`), a netns, a veth pair, a host gateway, and a sandbox IP. The
runtime recreates these host resources before start/restore, then:

- **Sandbox egress nftables** (`harness_egress`): applies the parsed egress rules.
  With `network.egress.allow_all: true` (the checked-in default) the output chain is
  policy-accept — the sandbox reaches the whole internet. With `allow_all: false` it
  is default-drop, allowing only loopback plus the parsed rules (Doris + the
  model-proxy alias). The "all" sentinel also drops the host-side inbound-drop rule
  while keeping masquerade.
- **Host egress/NAT** (`harness_gen_<id>`): forward + postrouting masquerade.
- **Pre-start probe** (`probeSandboxNetwork`): curls `<probe_url>/healthz` inside the
  netns until an accepted status (per `harness.probe`).

The egress policy is baked per generation at sandbox creation. The exfiltration
tradeoff of open vs. locked egress is covered in [security.md](./security.md);
`dns_policy` (`hostnames_only` in the checked-in config) gates name resolution.

## Startup handshake

After launch, the control plane runs a **bridge startup probe**
(`waitForBridgeStartupReadiness`) directly against the file queue before marking the
generation live — it must observe heartbeat + hello + `probe_network`. Inside the
sandbox, `harness-agent-entrypoint` waits for the digest-verified control manifest,
exports the runtime env, drops to the sandbox identity, and execs the bridge client
in claim-loop mode. See [sandbox-image.md](./sandbox-image.md).

## Idle release and checkpoint/restore

`MonitorIdleSessions` runs only when `checkpoint.auto_enabled` is true and network
mode is `sandbox`. That config check is the only policy gate: the per-row
`auto_checkpoint_enabled` columns are a creation-time copy and are not consulted
(see `ListIdleReleaseCandidates`). `releaseIdleGeneration` tears down an idle sandbox **without**
taking a `runsc` checkpoint image: the session moves to `checkpointed` (logical
"released"), resources are reclaimed, and the next message cold-starts a fresh
generation from the retained workspace + agent-home.

True `runsc` checkpoint/restore also exists:

- `Checkpoint` writes `checkpoint.img` / `pages.img` / `pages_meta.img` + a manifest.
- `resumeFromCheckpoint` runs `runsc restore -image-path ...`.
- Both are gated by strict checkpoint-metadata digest matching
  (`validateCheckpointRestore`) — a mismatch refuses restore.

Why not `--warm-sentry`: it isn't in the shipped `runsc` and was closed upstream for
security reasons; the low-latency direction is an orchestrator-level prewarm pool.
See [decisions/runsc-checkpoint-research.md](./decisions/runsc-checkpoint-research.md).

## Sandbox output and unexpected exits

Every `runsc run` process has a permanent output sink: each stdout/stderr line
is appended to `<run_dir>/logs/gen-<id>/sandbox.log` and the last 50 lines stay
in memory. When the process returns without the host having asked for it
(`ContainerExit.Intentional == false`), `server.HandleSandboxExit` retires a live
generation at once with `error_class = sandbox_exited` (or `sandbox_oom` when the
memcg says so) and the last stderr line as the reason, fails a running turn with a
transcript notice, and drops the session to `running_idle`. Lease expiry stays
the backstop; a lapse under the current owner is diagnosed as
`sandbox_heartbeat_lost`, and only a genuine owner change keeps the
`orchestrator_restart_*` classes.

## Cleanup and evidence

`DestroyGenerationResources` kills the container first (`runsc kill`/`delete`),
then removes the filesystem artifacts; a filesystem error never skips the kill.
The host-owned bridge sibling (`<bridge dir>.host`) is one of the targets.

`DestroyGenerationResources` deletes per-generation filesystem artifacts (control,
bundle, bridge, log dirs, checkpoint) and, in sandbox mode, the nft table / veth /
netns. Every recursive delete target is validated against its configured root and
the `gen-<generation_id>` component before `RemoveAll`; missing paths are treated as
success, invalid paths are hard errors. Cleanup records **absence evidence** (runsc
container absent, netns/veth/nft absent, filesystem absent) with digests, so the
`runtime_resource_instances` row can transition to `absent_verified`.

## Failure classification (`runtimeFailureClass`)

Runtime errors map to typed classes surfaced to the API: `sandbox_secret_disallowed`,
`shell_secret_disallowed`, `manifest_digest_mismatch`, `probe_failed_pre_start`,
`probe_failed_post_start`, `network_setup_failed`, `runtime_failed`.
