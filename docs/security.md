# Security & Isolation

> The threat model and the concrete controls that enforce it. Cross-refs:
> [sandbox-runtime.md](./sandbox-runtime.md) (isolation mechanics),
> [control-plane.md](./control-plane.md) (auth, proxy correlation),
> [decisions/gvisor-vs-firecracker.md](./decisions/gvisor-vs-firecracker.md) (why gVisor).

## Threat model

Internal users run AI agents that execute arbitrary code (Python, shell) against a
read-only Doris database. Agent code inside the sandbox is treated as untrusted: the
worst case is prompt injection driving the agent to escalate or exfiltrate. The
load-bearing control is that **model credentials never enter the sandbox** — that
holds regardless of the egress policy. Network egress is a separate, tunable dial
(open by default today; see below), not the credential boundary.

## Sandbox isolation (`sandbox-isolation-v1`)

Every generation runs under gVisor `runsc` (systrap platform) with:

- **Read-only rootfs**; the only writable surfaces are the exact `/workspace` and
  `/agent-home` binds, the bridge dir, and scratch tmpfs (`/tmp`, `/var/tmp`).
- **Non-root** agent process (uid/gid `65534` by default), **empty capability sets**,
  `noNewPrivileges`, PID limit 256, memory 1 GiB.
- **Strict mount allow-list** (`mount_plan.go`): exact binds only (no `rbind`), no
  duplicate destinations, and forbidden parent mounts — `/sessions`, `/agent-homes`,
  and `/harness-secrets` are rejected.
- **Per-generation network namespace** with a policy-driven egress nftables table.

## Egress policy (open by default, lockable)

Sandbox egress is governed by `network.egress` ([configuration.md](./configuration.md))
and baked per generation at sandbox creation. Two modes:

- **Open (`allow_all: true`, the checked-in default).** The sandbox output chain is
  policy-accept: agents can pip install, reach GitHub/PyPI, and search the web. This
  is a deliberate usability choice and it **removes the default-deny exfiltration
  boundary** — a compromised or prompt-injected sandbox can send data anywhere it can
  route to. The controls that still hold are the credential boundary below, gVisor
  isolation, and per-user session ownership; the network is no longer a containment
  layer.
- **Locked (`allow_all: false`).** Default-drop egress: the only allowed destinations
  are Doris (`172.16.0.138:9030`) and the model-proxy alias, with `dns_policy`
  controlling name resolution. Even a broken-out sandbox then has nowhere to send
  data. Choose this when the deployment handles sensitive data and does not need
  general internet access.

Neither mode changes the credential boundary — the sandbox never holds a real model
key in either case.

## Model credentials never enter the sandbox

This is the central credential boundary, independent of the egress policy above:

- The sandbox reaches the model provider only through a **hostname alias**
  (`harness-model-proxy.internal`, mapped to the host gateway in the sandbox's
  `/etc/hosts`). It holds a **dummy** `ANTHROPIC_API_KEY`; the real upstream key
  lives only in the separate model-proxy process.
- The entrypoint **rejects any secret mount** (exit 65) and unsets
  `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`; `validateGenerationDetails` returns
  `sandbox_secret_disallowed` if any secret path/id is present on a generation.
- There is one secret-grant spec (`model_provider` / `anthropic_messages`, exposure
  `proxy_only`, max TTL 24h) — it describes proxy-mediated access, not a mount.

The optional session-title LLM ([configuration.md](./configuration.md)) is a
**second, independent** host-side credential: the orchestrator calls its
OpenAI-compatible endpoint directly with the key from `config/title-llm.yaml`. It
does not touch the model proxy, the per-turn correlation, or the sandbox — the
sandbox never sees this key either, so it sits on the same side of the boundary as
the proxy's upstream key.

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
  (5 / 15 min). The client IP is derived from the trusted reverse proxy's
  `X-Real-IP` (nginx overwrites it with the real TCP peer), **not** the leftmost
  `X-Forwarded-For` entry, which a client can forge to rotate around the lockout.
  Set `HARNESS_TRUST_PROXY_HEADERS=0` when the orchestrator is exposed directly
  (no trusted proxy) so only `RemoteAddr` is used.
- **Credential changes need no restart**: `config/auth-users.yaml` is reloaded on
  change. Manage it with `harness-ctl user add|passwd|rm` (or `orchestrator user …`),
  which writes the file atomically at `0600` and revokes the affected user's login
  cookies by default (`--keep-sessions` opts out). A malformed, unreadable, or
  deleted file leaves the previously loaded credentials in force rather than
  falling back to open mode; removing the last user does reopen the gate, so it
  requires `--force`.
- **Global rate limit**: every request (except `/healthz`) passes a per-IP
  token-bucket limiter (~10 req/s sustained, burst 40); excess gets `429`. This
  is a coarse flood/scan guard on top of the login lockout.
- **Request body caps**: JSON endpoints wrap the body in `http.MaxBytesReader`
  (64 KiB for control endpoints, 1 MiB for agent messages); artifact uploads are
  capped separately (`MaxUploadBytes`, default 100 MiB; `MaxUploadFiles`, default
  2000) and online edits at `MaxEditBytes`. An oversized or slow body cannot exhaust
  memory. The public `http.Server` sets
  `ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout` and `MaxHeaderBytes` to blunt
  Slowloris-style slow-send attacks (`WriteTimeout` stays unset so SSE streams
  survive).
- **Sessions**: server-side, stored as a SHA-256 token hash in `auth_sessions`,
  revocable, 24h TTL. Cookie is HttpOnly, SameSite=Strict, `Secure` + `__Host-` over
  HTTPS; HSTS is emitted by the TLS-terminating nginx layer.
- **Owner scoping**: users act on only their own sessions. Writes — rename, destroy,
  send message, upload, artifact `PUT`, and share management — are owner-only, and a
  missing session or an owner mismatch returns `404` (not `403`), so sessions cannot
  be enumerated. Event streams re-check ownership.
- **Read grants**: four read-only routes (`GET /api/sessions/{id}`, `.../messages`,
  `.../artifacts`, and `GET /artifacts/{sessionID}/{path...}`) additionally admit a
  user holding a live grant in `session_shares`. Everything else stays owner-only,
  so a route that was never converted is still closed. A revoked or absent grant is
  the same `404` as a nonexistent session. A reader's session DTO drops `user_id`,
  `failure_reason`, and `error_class`; a `summary` grant also drops execution traces
  and intermediate tool steps.
- **Public link files**: `/shared/{token}/files/{path}` serves *snapshots*, not the
  live workspace. The path is an exact `share_link_files` lookup rather than a
  filesystem join, so an unshared path does not exist and there is no traversal
  surface; bytes come from a content-addressed store, so a later agent write cannot
  change what a published URL returns and the anonymous path never touches a live
  workspace (`VerifySessionWorkspaceVolume` is not in the loop). Snapshotting is
  capped per link by file count and total bytes, since the copy happens inside the
  create request. Blob reclamation is reference-counted: the sweep decides
  "unreferenced" and deletes the referencing rows in one transaction, and only the
  digests it returns are unlinked — deciding reachability outside that transaction
  would let a concurrent link creation have its file deleted underneath it.
- **Public links**: `/api/shared/{token}` is an unauthenticated subtree registered
  outside `requireAuth`, deliberately as a *separate* read path rather than by
  minting a cookie from a token — the authed routes never learn about an anonymous
  principal, so a missed check there cannot become anonymous access. It is
  GET-only (no CSRF surface), sends `Referrer-Policy: no-referrer` because the
  token is in the URL path, and its DTOs are built field by field so a field added
  to the authed DTO is not published by accident. Tokens are 32 random bytes stored
  only as a SHA-256 hash and returned exactly once at creation; rotating replaces
  the hash and kills the old URL immediately. Every link **must** expire (capped by
  `HARNESS_MAX_SHARE_LINK_TTL`, default 30 days) and unknown/expired/revoked all
  return the same `404`. Anonymous requests are not admitted to the event streams.
  Note the per-IP rate limiter is shared with logged-in users on the same address:
  a link forwarded widely inside one NAT can throttle that egress IP's authenticated
  traffic too.
- **Username reuse**: identity is a username string with no stable id, so
  `orchestrator user rm` revokes every share referencing the name (grants to them,
  grants they made, links they created) and **exits non-zero** if it cannot. A
  leaked cookie eventually expires; a share does not, so a silent failure here would
  be permanent unauthorized read access for the next namesake. `user passwd` does
  not revoke shares — same person, new credential.

## Output scrubbing

Public API and event payloads are scrubbed of internal identifiers and host paths
(`event_dto.go`, `session_dto.go`): `generation_id`, `driver_id`, `driver_state`,
`data_volumes`, `netns_path`, any `*_path`/`host_path`, `workspace`, etc. The browser
only ever sees product-level fields (`mode`, `mode_label`, status, timestamps).

## Artifact access safety

`GET /artifacts/{session}/{path}` streams only regular files under the verified
workspace, and rejects traversal, symlink components, symlink escape, directories,
and non-regular files. Path validation runs *before* any session lookup, so a
malformed path returns `400` without revealing whether the session exists. `PUT` on
the same path is the online editor (owner-only, `If-Unmodified-Since` guards against
clobbering a newer agent write); `POST /api/sessions/{id}/artifacts` is the multipart
upload. Every write path applies the same traversal/symlink validation as download.

`openArtifactFile` is shared by the download, the editor's staleness probe, and the
upload path, so it does **not** decide access itself: the caller passes the access
level it already established (readers pass the read gate's result, writers pass
owner). Relaxing a check inside that shared function would otherwise have relaxed
the write paths along with the read, and omitting the argument is a compile error
rather than a silent reliance on call ordering.

Downloaded content is served inert by default (`Content-Security-Policy:
default-src 'none'; sandbox`, `X-Frame-Options: DENY`, `nosniff`). HTML and PDF
artifacts are the exception: the workbench previews them in a same-origin iframe, so
they get a tailored CSP that permits same-origin framing and rendering
(`frame-ancestors 'self'`) while still forbidding all outbound
connect/img/font/form/nested-frame behavior — a malicious artifact can render but
cannot phone home.

## Single-owner control plane

One orchestrator per data root, enforced by both a flock pidfile and a DB owner row
with a 5s heartbeat; all lease/reaper mutations are owner-fenced. This prevents two
orchestrators from racing on the same SQLite DB and runtime resources.

## Browser response headers

The Next.js workbench sets a baseline security header set on every response
(`frontend/next.config.mjs`): a pragmatic **CSP** (`default-src 'self'`,
`frame-ancestors 'none'`, `object-src 'none'`; `script`/`style` allow
`'unsafe-inline'` because Next.js injects inline bootstrap and the app ships an
inline theme script), plus `X-Frame-Options: DENY`, `X-Content-Type-Options:
nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`, a restrictive
`Permissions-Policy`, and `Cross-Origin-Opener-Policy: same-origin`. Agent
output is rendered via react-markdown (no `rehype-raw`) and highlight.js
(HTML-escaped), so stored-HTML XSS is already contained; the CSP is defense in
depth. Orchestrator JSON responses carry `nosniff`, and artifact downloads add
`Content-Security-Policy: default-src 'none'; sandbox` + `X-Frame-Options: DENY`
so a malicious artifact cannot be framed or sniffed into active content.

## Deployment TLS

The orchestrator serves plain HTTP; production **must** terminate TLS at a reverse
proxy (nginx) and forward `X-Forwarded-Proto https` so Secure cookies engage. The
trusted proxy must also overwrite `X-Real-IP` with `$remote_addr` for rate-limit
keying and emit `Strict-Transport-Security` on HTTPS responses. The publish
deployment binds the frontend to `127.0.0.1:9001` behind nginx. See
[operations.md](./operations.md).

## Sensitive local material

The `doris-query` skill's `SKILL.md` embeds live Doris connection details (host,
read-only user, password). It ships baked into the rootfs, so treat the rootfs/image
as sensitive. Note the Doris user is read-only, but with open egress the sandbox can
still route those credentials (and query results) off-box; lock egress
(`allow_all: false`) for deployments where that matters.

Any agent in any sandbox can `cat` that file, and agents routinely echo it into
traces and share links. Treat the Doris credential as compromised-by-design:
every user of this platform effectively holds it.

**A gateway that removes this is built but NOT deployed** (as of 2026-07-29). It
lives outside this repo in `/home/agent-runtime-platform/data-proxies/doris/`
(design in its `PLAN.md`, operation in its `README.md`, deployment risk in its
`PHASE2-RISK.md`). Nothing in `main/` routes through it yet — no config key, no
systemd unit installed, no skill or rootfs change — so everything above still
describes the live system. Do not describe the credential as hidden until that
work lands.

When it does land, two limits will still hold and are worth stating plainly: the
gateway hides the *credential*, not the *data*, so under open egress a
prompt-injected agent can still `SELECT` and POST the rows elsewhere; and the
already-leaked credential stays valid until someone rotates it, so an agent
reading an old trace in its own workspace could still recover the old value.
Rotation is later hardening, deliberately not a rollout gate.

## Residual limitations

gVisor is one tier weaker than a KVM microVM (Firecracker) but far stronger than
shared-kernel containers; for the internal-users + read-only-DB threat model it is
sufficient (see
[decisions/gvisor-vs-firecracker.md](./decisions/gvisor-vs-firecracker.md)). With the
default open egress, the sandbox network is not an exfiltration barrier — the
credential boundary and gVisor isolation are. Tenant authz beyond per-user session
ownership, credential rotation/GC, per-tenant egress policy, and resource limits are
future work.
