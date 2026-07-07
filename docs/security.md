# Security & Isolation

> The threat model and the concrete controls that enforce it. Cross-refs:
> [sandbox-runtime.md](./sandbox-runtime.md) (isolation mechanics),
> [control-plane.md](./control-plane.md) (auth, proxy correlation),
> [decisions/gvisor-vs-firecracker.md](./decisions/gvisor-vs-firecracker.md) (why gVisor).

## Threat model

Internal users run AI agents that execute arbitrary code (Python, shell) against a
read-only Doris database. The worst case is prompt injection driving the agent to
exfiltrate data or escalate. The design assumes agent code inside the sandbox is
untrusted; the controls below ensure that even a fully compromised sandbox cannot
reach credentials or the network beyond Doris and the model proxy.

## Sandbox isolation (`sandbox-isolation-v1`)

Every generation runs under gVisor `runsc` (systrap platform) with:

- **Read-only rootfs**; the only writable surfaces are the exact `/workspace` and
  `/agent-home` binds, the bridge dir, and scratch tmpfs (`/tmp`, `/var/tmp`).
- **Non-root** agent process (uid/gid `65534` by default), **empty capability sets**,
  `noNewPrivileges`, PID limit 256, memory 1 GiB.
- **Strict mount allow-list** (`mount_plan.go`): exact binds only (no `rbind`), no
  duplicate destinations, and forbidden parent mounts — `/sessions`, `/agent-homes`,
  and `/harness-secrets` are rejected.
- **Per-generation network namespace** with default-drop egress nftables. The only
  allowed egress is Doris (`172.16.0.138:9030`) and the model-proxy alias; DNS is
  `hostnames_only`. Even a broken-out sandbox has nowhere to send data.

## Model credentials never enter the sandbox

This is the central credential boundary:

- The sandbox reaches the model provider only through a **hostname alias**
  (`harness-model-proxy.internal`, mapped to the host gateway in the sandbox's
  `/etc/hosts`). It holds a **dummy** `ANTHROPIC_API_KEY`; the real upstream key
  lives only in the separate model-proxy process.
- The entrypoint **rejects any secret mount** (exit 65) and unsets
  `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`; `validateGenerationDetails` returns
  `sandbox_secret_disallowed` if any secret path/id is present on a generation.
- There is one secret-grant spec (`model_provider` / `anthropic_messages`, exposure
  `proxy_only`, max TTL 24h) — it describes proxy-mediated access, not a mount.

## Proxy correlation (authorization per turn)

The model proxy does not blindly forward. For each request it calls the
orchestrator's **Unix-socket** correlation API
(`/internal/proxy/requests/{start,finish}`), which is:

- **Authenticated by SO_PEERCRED** against `harness.proxy_service_identity` — only a
  process with the configured uid/gid can call it; it is never exposed over TCP.
- **Correlated by source IP**: a model request is authorized only if the sandbox's
  real source IP maps to a live `active_model_request_contexts` row, which exists
  only between `ack_turn_started` and turn completion, and only if the generation's
  sandbox contract allows model access.

So a sandbox has model access **only during its own active turn**, and the sandbox's
self-reported source IP is a diagnostic, not an authority.

## Sandbox contract (integrity)

Each generation gets an immutable, canonicalized, digested **sandbox contract**
(`sandbox-isolation-v1`, schema 2) describing driver, provider, mounts, network
identity, credential policy, data volumes, and input-evidence digests
(`runtime_config_digest`, `agent_manifest_digest`). The control manifest handed to
the sandbox is `{payload, digest}` and is re-verified inside the sandbox before any
env is trusted. The runsc binary is pinned (path + sha256) and re-verified before
each launch/checkpoint/cleanup, and the driver binary's sha256 is checked against the
image manifest during resolution ([drivers.md](./drivers.md)).

## Authentication & multi-user isolation

- **Login**: username/password verified against PBKDF2-SHA256 hashes (600k
  iterations) in `config/auth-users.yaml`; or `HARNESS_LAB_PASSWORD`; or open mode
  (single `lab` user, dev only). Failures are rate-limited per `username|IP`
  (5 / 15 min).
- **Sessions**: server-side, stored as a SHA-256 token hash in `auth_sessions`,
  revocable, 24h TTL. Cookie is HttpOnly, SameSite=Strict, `Secure` + `__Host-` over
  HTTPS (with HSTS).
- **Owner scoping**: users see and act on only their own sessions; a missing session
  or an owner mismatch returns `404` (not `403`), so sessions cannot be enumerated.
  Event streams and artifact downloads re-check ownership.

## Output scrubbing

Public API and event payloads are scrubbed of internal identifiers and host paths
(`event_dto.go`, `session_dto.go`): `generation_id`, `driver_id`, `driver_state`,
`data_volumes`, `netns_path`, any `*_path`/`host_path`, `workspace`, etc. The browser
only ever sees product-level fields (`mode`, `mode_label`, status, timestamps).

## Artifact download safety

`GET /artifacts/{session}/{path}` streams only regular files under the verified
workspace, re-checking ownership first, and rejecting traversal, symlink components,
symlink escape, directories, and non-regular files.

## Single-owner control plane

One orchestrator per data root, enforced by both a flock pidfile and a DB owner row
with a 5s heartbeat; all lease/reaper mutations are owner-fenced. This prevents two
orchestrators from racing on the same SQLite DB and runtime resources.

## Deployment TLS

The orchestrator serves plain HTTP; production **must** terminate TLS at a reverse
proxy (nginx) and forward `X-Forwarded-Proto https` so Secure cookies and HSTS
engage. The publish deployment binds the frontend to `127.0.0.1:9001` behind nginx.
See [operations.md](./operations.md).

## Sensitive local material

The `doris-query` skill's `SKILL.md` embeds live Doris connection details (host,
read-only user, password). It ships baked into the rootfs. Keep egress locked to the
Doris host and treat the rootfs/image as sensitive.

## Residual limitations

gVisor is one tier weaker than a KVM microVM (Firecracker) but far stronger than
shared-kernel containers; for the internal-users + read-only-DB + egress-allow-list
threat model it is sufficient (see
[decisions/gvisor-vs-firecracker.md](./decisions/gvisor-vs-firecracker.md)). Tenant
authz beyond per-user session ownership, credential rotation/GC, per-tenant egress
policy, and resource limits are future work.
