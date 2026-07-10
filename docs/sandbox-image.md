# Sandbox Image

> How the rootfs the sandbox boots is built, what runs inside as PID 1, and how the
> in-sandbox side of the bridge and drivers work. Code in `sandbox-image/`. The
> host side that launches this image is [sandbox-runtime.md](./sandbox-runtime.md).

## Layout

```
sandbox-image/
  build-rootfs.sh                         # OCI rootfs builder
  files/                                   # overlay, rsync'd verbatim into the rootfs
    usr/local/bin/harness-agent-entrypoint # PID 1 shim (sh)
    usr/local/bin/harness-bridge-client    # in-sandbox bridge + driver runners (py3)
    usr/local/bin/harness-shell-agent      # PTY shell driver (py3)
    usr/local/lib/harness_manifest.py      # control-manifest load/verify
    usr/local/share/harness/pi-system-prompt.md
    usr/local/share/harness-skills/doris-query/…
    usr/local/share/harness-skills/matplotlib-fonts/…
  tests/test_harness_bridge_client.py      # pins bridge/driver/entrypoint behavior
```

## Building the rootfs (`build-rootfs.sh`)

Produces an Ubuntu (`noble`) OCI rootfs directory. Key env knobs:

- `ROOTFS_DIR` — output dir (default `sandbox-image/rootfs`).
- `SANDBOX_AGENT_DRIVERS` — CSV of drivers to install (default `pi`; accepts `pi`,
  `sh`). The checked-in lab default needs `SANDBOX_AGENT_DRIVERS=pi,sh`.
- `FORCE` — `0` (default) reuses an existing rootfs and only re-syncs the overlay +
  regenerates the manifest (no root, no debootstrap); `1` does a full rebuild
  (requires root + `debootstrap`).

Full-build steps: `debootstrap` → apt (`ca-certificates curl fonts-wqy-microhei
fonts-wqy-zenhei git nodejs npm python3 python3-pip python3-venv xz-utils`) → pip
(`matplotlib pandas pymysql`) → install a modern Node for Pi if selected → install
the selected driver CLIs (`pi` via npm pinned to 0.77.0, `sh` uses the bundled
shim) → `rsync` the overlay → generate `/etc/harness-image/agents.json`
→ sanitize.

The two `fonts-wqy-*` packages (WenQuanYi Zen Hei / Micro Hei) give matplotlib and
Pillow CJK glyph coverage — without them, Chinese text in generated charts/images
renders as tofu boxes. See the **matplotlib-fonts** skill below.

**Sanitize** scrubs any host/session state (`/workspace`, `/agent-home`,
`/sessions`, `/agent-homes`, `/harness-secrets`, stray gVisor filestore files)
and recreates empty `/workspace` and `/agent-home` mount points, so the image
is a clean template.

## Agent image manifest (`/etc/harness-image/agents.json`)

Generated per selected driver with `schema_version: 1` and, for each driver:
`driver_id`, `label`, `kind`, `binary_path`, `installed_binary_digest` (sha256 of the
installed binary), `bridge_protocol`/`version`, `turn_input_schema`, `output_schema`,
`model_access`, and package facts. Pi additionally records `package_version`,
`shasum`, `integrity`, `event_schema_version`, `node_version`, and its
config/writable-state paths. The orchestrator verifies this manifest against the
driver registry before allowing a driver — see the manifest validation section in
[drivers.md](./drivers.md).

`harness_manifest.py` is a small security module: `load_control_manifest(path)`
requires `{payload, digest}`, recomputes the sha256 of the canonical payload, and
`hmac.compare_digest`-checks it — a mismatch is `SystemExit`. This same digest scheme
is what the entrypoint trusts.

## Entrypoint (`harness-agent-entrypoint`, PID 1)

POSIX `sh` boot sequence:

1. Require `HARNESS_AGENT_UID` / `HARNESS_AGENT_GID` (no hardcoded default).
2. Poll for the control file `/harness-control/session.json`.
3. Load it via `harness_manifest.load_control_manifest` (digest-verified) and export
   the runtime env (`SESSION_ID`, `GENERATION_ID`, `HARNESS_DRIVER_ID`,
   `SESSION_WORKSPACE`, `HARNESS_AGENT_HOME`, `ANTHROPIC_BASE_URL` = the proxy alias,
   Pi's `PI_*` gates from a nested `driver_runtime` object, etc.).
4. Cross-check any `HARNESS_EXPECTED_*` values; mismatch → exit 65.
5. **Reject secret mounts**: if any secret-mount env is set, exit 65; then
   `unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN`. Model credentials are host-only.
6. Validate `/workspace` and `/agent-home` are the exact expected mounts.
7. Drop to the sandbox identity (via `setpriv --clear-groups` for agent drivers) and
   exec `harness-bridge-client` in `claim-loop` mode (or `probe` / `heartbeat-loop`).

## Bridge client (`harness-bridge-client`)

The in-sandbox counterpart to the host bridge processor
([control-plane.md](./control-plane.md)). It speaks `harness_bridge_v2` over the file
queue under `/harness-control/bridge`:

- **Transport** (`Queue`): messages are `NN…N.json` files (20-digit sequence),
  written to `tmp/` then `os.link`'d into the queue with an fsync — atomic and
  crash-safe. Heartbeat and `checkpoint-ready` are timestamp files.
- **`claim-loop`**: heartbeat → hello → proxy `/healthz` probe →
  `probe_network` → mark checkpoint-ready → build the driver runner → loop:
  resume a leased turn (from `hello_ack`) or `claim_next_turn`; on a grant, run the
  driver's `run_turn(content, emit)` where `emit` sends `emit_output`; finish with
  `ack_turn_completed`.
- **`probe`**: the pre-start reachability check.

`sandbox_source_ip()` reads the first non-loopback address (ignoring any env claim),
so the host correlates the model proxy by the real sandbox IP.

**Driver runners** (`RUNNER_FACTORIES`): `PiRPCTurnRunner`, `ShellTurnRunner`, and a
`native_events_probe` test runner. Their command lines and behaviors are described
in [drivers.md](./drivers.md). All model-access runners get only
`ANTHROPIC_BASE_URL` (the proxy alias) and a dummy API key — never a real
credential.

## Shell agent (`harness-shell-agent`)

A PTY-backed shell driver. Prefers `/bin/bash` (falls back to `sh`), disables echo /
history / bracketed paste, and installs a `PROMPT_COMMAND` that prints a unique
sentinel `__HARNESS_PROMPT_<uuid>__[<exit>]__` after each command. It reads
`{"type":"turn","content":...}` / `{"type":"interrupt"}` frames and emits
`harness.shell_output` and `harness.turn_done` (with exit code). Output schema
`shell_pty_v1`, no model access.

## System prompt & skills

Skills are **baked into the image** (rsync'd overlay), not dynamically mounted, and
appear at `/usr/local/share/harness-skills/`. The Pi system prompt
(`pi-system-prompt.md`) sets the identity "BatteryGPT" and, for Doris/`vhr_data`
questions, directs the agent to read the doris-query SKILL and follow read-only
rules: only `SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN`; prefer metadata, `COUNT(*)`, small
`LIMIT`; never `SELECT *` on wide/raw tables; stream CSV exports with `fetchmany` +
atomic rename. It is injected into Pi via `--append-system-prompt`.

The **doris-query** skill (`SKILL.md`) documents the read-only Doris connection and
instructs the agent to discover the current `vhr_data` schema live via
`information_schema` every session, rather than relying on a fixed table/column
list — the schema changes over time. Note: `SKILL.md` embeds live DB connection
details — treat it as sensitive local material and keep egress locked to the Doris
host (see [security.md](./security.md)).

`sandbox-image/files/usr/local/share/harness-skills/` is **git-ignored**, not
checked in, because doris-query's `SKILL.md` embeds a live credential. Before
running `build-rootfs.sh`, restore that directory locally (from your own secrets
store or a prior local copy) so the overlay rsync has something to pick up —
without it, the doris-query and matplotlib-fonts skills are simply absent from
the built image.

The **matplotlib-fonts** skill (`SKILL.md`) documents the two installed WenQuanYi CJK
fonts (`wqy-zenhei.ttc`, `wqy-microhei.ttc`) and how to select them in matplotlib
(`rcParams["font.sans-serif"]`) and Pillow (`ImageFont.truetype`), so Chinese text in
generated charts/images doesn't render as tofu boxes.

## Tests (`tests/test_harness_bridge_client.py`)

Pins: the `FORCE=0` reuse path regenerates the Pi manifest correctly; queue numeric
ordering and heartbeat/checkpoint-ready markers; the `hello`/claim/ack/emit protocol
and resume sequence numbering; Pi runner command line, config-materialization
read-only/symlink rejection, stderr handling, and sidecar restore; the manifest
digest fixture; and entrypoint invariants (required uid/gid, secret-mount
rejection, workspace/agent-home ordering).
