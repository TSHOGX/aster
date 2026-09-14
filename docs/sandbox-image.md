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

### Per-host build config (`sandbox-image/build.env`)

Anything specific to *where* the build runs or *where the image will run* is read
from `sandbox-image/build.env` (gitignored; override the path with
`SANDBOX_BUILD_ENV`). The committed defaults are upstream-canonical, so a build
with no env file is correct — it just fetches from upstream and bakes public
resolvers. Copy `build.env.example` and fill in per host:

| Variable | Default | Purpose |
|---|---|---|
| `UBUNTU_MIRROR` | `archive.ubuntu.com` on amd64/i386, `ports.ubuntu.com` elsewhere | apt/debootstrap mirror. Only x86 is served by `archive`. |
| `UBUNTU_COMPONENTS` | `main,universe` | debootstrap components |
| `PIP_INDEX_URL` / `PIP_TRUSTED_HOST` | unset → pypi.org | pip index |
| `NPM_REGISTRY` | unset → registry.npmjs.org | registry for the in-chroot Pi install |
| `NODE_DIST_BASE_URL` | `https://nodejs.org/dist` | Node tarball source |
| `SANDBOX_DNS_SERVERS` | `1.1.1.1 8.8.8.8` | resolvers written to the image's `/etc/resolv.conf` |
| `SANDBOX_DNS_SEARCH` | unset | `search` line in the same file |
| `SANDBOX_BUILD_DNS_HOOK` | unset | executable run after debootstrap with the rootfs dir as `$1` |

`/etc/resolv.conf` is **rendered at build time**, not committed — the sandbox falls
back to it whenever the orchestrator is not projecting a per-generation
`/etc/hosts` (that projection is conditional; see `requiresNetworkHostsProjection`),
so the resolvers must be reachable wherever the image runs. A cloud deployment
points them at its VPC resolvers via `build.env`.

`SANDBOX_BUILD_DNS_HOOK` exists for build hosts whose own DNS the chroot cannot
use — e.g. a fake-ip proxy that answers every query with an unroutable
`198.18.0.0/15` address. `dev/build-dns-pin.sh` is the reference implementation
(resolves the build's download hosts over DoH and pins them in the chroot's
`/etc/hosts`); it no-ops on a host with working DNS. Nothing it writes reaches the
image — sanitize resets `/etc/hosts`.

Full-build steps: `debootstrap` → apt → pip → bake the DuckDB `httpfs` extension →
install a modern Node for Pi if selected → install the selected driver CLIs (`pi`
via npm pinned to 0.77.0, `sh` uses the bundled shim) → `rsync` the overlay →
generate `/etc/harness-image/agents.json` → sanitize.

The apt and pip sets live in `APT_PACKAGES` / `PIP_PACKAGES` at the top of
`build-rootfs.sh`; read them there rather than from a copy here. Two notes on
what's in them:

- **`mysql-client-core-8.0`** gives the `mysql` CLI that the **doris-query** skill
  documents as its quick-check path. Upstream MySQL's client rather than
  `mariadb-client`, deliberately: Doris FE reports itself as `5.7.99` and offers
  only `mysql_native_password`, which is the path mysql-client negotiates
  natively, whereas mariadb-client diverges on auth-plugin preference and
  `--ssl-mode` defaults. Worth keeping to one implementation — a credential
  gateway that terminates the handshake has to match a known client.
- **Nothing is version-pinned**; every rebuild takes current releases. That is a
  deliberate call — the two packages that talk to something outside the image
  (DuckDB to OBS, pymysql to Doris FE) are both self-aligning rather than
  pin-dependent: the `httpfs` directory name is derived from the engine version at
  build time (see below), and pymysql negotiates the FE handshake by protocol, not
  by version. So a rebuild can move minor versions; treat that as expected and
  re-run the compatibility checks after one, rather than freezing versions.

Reading either version back has a trap in both directions, so verify per package:
`pymysql.__version__` reports `2.2.8`, a deliberate `mysqlclient` impersonation
for Django's version check — the real value is `pymysql.VERSION_STRING`, which
matches dist-info. And `duckdb.__version__` is the Python wrapper's version read
from dist-info, not the engine's — that one is `duckdb.__duckdb_version__`. There
is no single symbol to trust across both.

The two `fonts-wqy-*` packages (WenQuanYi Zen Hei / Micro Hei) give matplotlib and
Pillow CJK glyph coverage — without them, Chinese text in generated charts/images
renders as tofu boxes. See the **matplotlib-fonts** skill below.

**Sanitize** scrubs any host/session state (`/workspace`, `/agent-home`,
`/sessions`, `/agent-homes`, `/harness-secrets`, stray gVisor filestore files)
and recreates empty `/workspace` and `/agent-home` mount points, so the image
is a clean template. It also resets `/etc/hosts` to localhost-only, dropping
whatever the build host resolved during the build. Reset rather than delete: a
generation without the hosts projection still needs `localhost` to resolve, and
gVisor's file bind mount needs the destination to exist.

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
- **`claim-loop`**: acquire the per-generation producer lock → heartbeat → hello → proxy `/healthz` probe →
  `probe_network` → mark checkpoint-ready → build the driver runner → loop:
  resume a leased turn (from `hello_ack`) or `claim_next_turn`; on a grant, run the
  driver's `run_turn(content, emit)` where `emit` sends `emit_output`; finish with
  `ack_turn_completed`.

The client resumes output sequence numbers from both the host's committed
`hello_ack` boundary and any still-pending `emit_output` files in its local
outbox. The lock prevents two claim-loop producers from sharing one generation;
the combination makes reconnects crash-safe without reusing a sequence number.
- **`probe`**: the pre-start reachability check.

`sandbox_source_ip()` reads the first non-loopback address (ignoring any env claim),
so the host correlates the model proxy by the real sandbox IP.

**Driver runners** (`RUNNER_FACTORIES`): `PiRPCTurnRunner`, `ShellTurnRunner`, and a
`native_events_probe` test runner. Their command lines and behaviors are described
in [drivers.md](./drivers.md). All model-access runners get only
`ANTHROPIC_BASE_URL` (the proxy alias) and a dummy API key — never a real
credential.

### Turn control (host → sandbox, mid-turn)

Stop and per-row send have to reach a turn that is *already executing*, and the
bridge cannot carry them: it is request/response with the sandbox as the only
requester, and during a turn the turn thread is blocked reading the driver, so
nothing is polling the inbox. So the host writes `inbox/turn-control.json` — a
versioned, ordered **log** of commands, each with a `token` and the `turn_id` it
targets — and `start_turn_control_watcher` reads it on a second daemon thread for
exactly as long as the turn runs.

- The name is deliberately **not** a sequence name. Both queue readers accept only
  the fixed-width `00000000000000000001.json` form and the host's prune only
  deletes sequence-named files, so this file is invisible to the queue machinery
  and safe from its cleanup: no new mount, no contract change.
- A **log**, not a slot: the watcher polls every 200 ms, and two commands written
  inside one interval would leave only the second — for `steer` that is a message
  the user typed, silently dropped after the host reported it delivered.
- `token` gives at-most-once (the sandbox cannot delete a read-only file);
  `turn_id` keeps a command noticed late from reaching the turn that came after
  its target. `abort` ends the watch, `steer` does not — the turn keeps running
  and the user may steer again.
- Writes to the driver's stdin are serialized under a lock, because the watcher
  thread writes while the turn thread is blocked reading the same process.

Schema version 1, shared with the host writer
(`orchestrator/internal/bridge/turncontrol.go`); anything else is refused rather
than guessed at, so a host and a rootfs that disagree fail visibly instead of
dropping the user's Stop.

## Shell agent (`harness-shell-agent`)

A PTY-backed shell driver. Prefers `/bin/bash` (falls back to `sh`), disables echo /
history / bracketed paste, and installs a `PROMPT_COMMAND` that prints a unique
sentinel `__HARNESS_PROMPT_<uuid>__[<exit>]__` after each command. It reads
`{"type":"turn","content":...}` / `{"type":"interrupt"}` frames and emits
`harness.shell_output` and `harness.turn_done` (with exit code). Output schema
`shell_pty_v1`, no model access.

## System prompt & skills

The Pi system prompt (`pi-system-prompt.md`, injected via `--append-system-prompt`)
is now a **minimal bootstrap**: it states the "BatteryGPT" identity and directs the
agent to read `/agents/CONSTITUTION.md` and consult the `/agents` knowledge base
before recurring work. It carries no domain rules and no `harness-skills/`
references — those moved into the mounted, evolving KB. See
[agents-kb.md](./agents-kb.md) for the full design.

Skills now live in the **`/agents` knowledge base** (a read-only bind mount whose
source is a content-addressed snapshot the nightly loop republishes), seeded from
the baked overlay at `sandbox-image/files/usr/local/share/agents-kb-seed/`:

- **doris-query** (`skills/doris-query/SKILL.md`, `status: in-use`) — read-only
  Doris/`vhr_data` access: live `information_schema` discovery, safe SQL
  (`SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN` only), streaming large exports. Embeds a
  live credential, so the real `SKILL.md` is **git-ignored**; a redacted
  `SKILL.md.example` is committed. Copy it to `SKILL.md` with real values (or
  provision on the host) before `build-rootfs.sh`, and keep egress locked to the
  Doris host (see [security.md](./security.md)).
- **obs-parquet-duckdb** (`skills/obs-parquet-duckdb/SKILL.md`, `status: in-use`) —
  read raw vehicle-signal Parquet on Huawei Cloud OBS **in place** with DuckDB
  `httpfs` (`s3_url_style='vhost'`, the one non-obvious requirement; `path`/
  `virtual` 404). Covers the 5,844-column sparse wide-table layout, the
  metadata→schema-search→`COLUMNS()`→filter workflow, glob/sampling costs, and the
  hard rule that source objects are **never downloaded to local disk** (~17 MB ×
  1.1M files). Embeds a live read-only OBS key, so same handling as doris-query:
  real `SKILL.md` **git-ignored**, redacted `SKILL.md.example` committed.
- **matplotlib-cjk-fonts** (`skills/matplotlib-cjk-fonts/SKILL.md`, `status:
  in-use`) — select the installed WenQuanYi CJK fonts in matplotlib/Pillow so
  Chinese chart text doesn't render as tofu boxes.

`obs-parquet-duckdb` depends on a baked DuckDB extension: `build-rootfs.sh`'s
`install_duckdb_extensions` installs `httpfs` into
`/usr/local/share/duckdb-extensions/v<engine-version>/<platform>/`, so a session
does `LOAD httpfs` against that `extension_directory` with **no network fetch**.

That path segment is the **bundled engine** version (`duckdb.__duckdb_version__`),
and DuckDB resolves extensions from its own version at load time. So the directory
name and the engine must agree or `LOAD httpfs` fails with `Extension not found` —
loudly, never by silently falling back to a network fetch. They stay matched
without a pin because `install_duckdb_extensions` reads
`duckdb.__duckdb_version__` from the just-installed wheel to name the directory —
so whatever version a rebuild lands on, the extension is fetched and stored for
that engine. A working `LOAD httpfs` inside a sandbox is itself evidence the two
agree, which is the check worth running after a rebuild.

> **Deprecated:** `sandbox-image/files/usr/local/share/harness-skills/` (still
> git-ignored) is superseded by the `/agents` KB seed above and is no longer
> referenced by the prompt. Existing deployed copies are harmless; the directory
> will be removed in a later image cleanup.

## Tests (`tests/test_harness_bridge_client.py`)

Pins: the `FORCE=0` reuse path regenerates the Pi manifest correctly; queue numeric
ordering and heartbeat/checkpoint-ready markers; the `hello`/claim/ack/emit protocol
and resume sequence numbering; Pi runner command line, config-materialization
read-only/symlink rejection, stderr handling, and sidecar restore; the manifest
digest fixture; and entrypoint invariants (required uid/gid, secret-mount
rejection, workspace/agent-home ordering).

Turn control has its own group, and two of those tests pin properties that are
easy to regress silently: that the control file is **invisible to the message
queue** (both queue readers reject its non-seq name), and that `set_turn_context`
clears a previous abort marker — without it the turn after a stopped one is
cancelled the moment it starts. The rest cover reading a malformed log without
raising, command ordering, applying abort vs steer to pi, the watcher surviving a
runner that raises, and the watcher stopping at the first abort.
