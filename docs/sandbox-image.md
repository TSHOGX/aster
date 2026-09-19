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
  `sh`). Select the same driver set as your application configuration; release builds include `pi,sh`.
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

Local image builds can render a resolver from their private build environment.
Generic release images contain no deployment DNS; managed runtime specs mount the
operator's `HARNESS_RESOLV_CONF` read-only. The `/etc/hosts` alias projection is a
separate mechanism and does not supply DNS resolvers.

`SANDBOX_BUILD_DNS_HOOK` exists for build hosts whose own DNS the chroot cannot
use — e.g. a fake-ip proxy that answers every query with an unroutable
`198.18.0.0/15` address. `dev/build-dns-pin.sh` is the reference implementation
(resolves the build's download hosts over DoH and pins them in the chroot's
`/etc/hosts`); it no-ops on a host with working DNS. Nothing it writes reaches the
image — sanitize resets `/etc/hosts`.

Full-build steps: `debootstrap` → apt → pip → bake the DuckDB `httpfs` extension →
install a modern Node for Pi if selected → install the selected driver CLIs (`pi`
via npm pinned to 0.77.0, `sh` uses the bundled shim) → assert Pi's turn-control
RPC contract → `rsync` the overlay → generate `/etc/harness-image/agents.json` →
sanitize.

`verify_pi_rpc_turn_control` runs after installation and during finalization,
including reuse of an existing image, when `pi` is selected. Stop and per-row steer are not our primitives: the bridge client
writes exactly `{"type":"abort"}` and `{"type":"steer","message":…}` to Pi's
stdin, so the build greps Pi's own `dist/modes/rpc/rpc-types.d.ts` for both
shapes and fails the rebuild if either is gone. Without it a Pi upgrade that
renamed or reshaped them would break Stop *silently* — the client keeps writing,
Pi ignores the line, no error and no event reach the user. Verify rather than
freeze, as with `httpfs`: the version pin would not catch a reshape within one
version.

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
- Release builds pin Python dependencies with hashes in
  `sandbox-image/requirements.lock`, and record toolchain/Ubuntu snapshot inputs
  in `scripts/release/toolchains.json`. Local exploratory builds use the explicit
  package list. Any dependency update requires rebuilding and artifact smoke;
  matching DuckDB engine/extension versions is checked independently.

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

## Bootstrap prompt and private knowledge

The baked Pi prompt describes a generic engineering assistant and directs it to
`/agents/CONSTITUTION.md` and `facts/INDEX.md`. It contains no organization identity,
private schema or data-store preference. The generic seed includes the constitution,
index and portable font skill. Integration `.example` files are templates only.

Provision actual skills, corpora and domain rules in the deployment's private KB
snapshot. Never put upstream credentials into a shared image or public skill.
The runtime mounts a resolved immutable snapshot; existing generations stay
pinned and later generations use newly approved snapshots. See
[memory and knowledge](agents-kb.md).

The image includes DuckDB's matching `httpfs` extension under
`/usr/local/share/duckdb-extensions/v<engine-version>/<platform>/`. The installer
uses the bundled engine version and the hosted artifact smoke verifies `LOAD
httpfs` without network access. Release Python dependencies are hash-locked;
local exploratory builds may use the explicit package list instead.

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

## Khora template build and launch

`sandbox-image/build-khora-template.sh NEW_TEMPLATE_DIRECTORY` builds a fresh
Pi/shell rootfs, installs `sandboxd` from the pinned Khora module, and writes the
application driver manifest with engine revision and guest component hashes.
The destination must not exist. Run it as root in a disposable builder with the
private Go dependency already available. Template directories are immutable
release inputs; never rebuild one under retained sandboxes.

Khora owns PID 1. The application starts `/usr/local/bin/harness-khora-launcher`
with session, generation and driver arguments. The launcher uses `/root` as HOME,
`/workspace` as working directory, `/harness-bridge` for execution-partitioned
journals, `/harness-config` for immutable configuration sources, and `/root/.agents`
for shared read-only assets. Pi native history lives in the retained overlay.
The launcher materializes model/settings files into `/root/.pi/agent`, whose
directory remains writable for Pi's lock files. Model credentials remain outside
the guest; the model config requires a non-secret placeholder. Shared `AGENTS.md`
is linked into Pi's global context location only when session content does not
already occupy it.

The builder removes the old application init wrapper and `/agent-home` layout
from the Khora template. The existing local-backend rootfs builder remains
available during migration. The engine and application manifests have distinct
roles: engine template identity is immutable storage input; application metadata
pins the driver, bridge and launcher that the product expects to start.

`harness-workspace-helper` provides confined guest read, replace and sync
operations for `/workspace` and Pi's native session directory. It walks entries
with `O_PATH|O_NOFOLLOW`, checks each type, and opens the pinned descriptor for
I/O. This explicit check is required by the tested gVisor behavior; a directory
open with `O_DIRECTORY|O_NOFOLLOW` alone was insufficient. Replacement verifies
its declared size and SHA-256, synchronizes file metadata, renames within the
pinned parent and synchronizes that parent. Post-rename sync failure is reported
as uncertain, so the application must reconcile its operation before retrying.
The helper is a mechanism, not a lifecycle or mutation-ledger owner.

At a completed Pi RPC boundary, the Khora bridge synchronizes the selected native
JSONL and its parent before producing the driver-state update. A failed barrier
fails that advancement. This establishes successful fsync calls on the tested
guest; it does not claim a power-loss simulation or prevent an independently
running guest writer from modifying the file afterward.

The `snapshot-native` helper operation preserves the completed JSONL under a
state-specific name in `.harness-completed`. It uses a separate inode, verifies
the session header, bounds copies to 512 MiB, and rejects a changed source or an
existing destination with different bytes. File and parent synchronization
precede success. Snapshot retries and symlink rejection use the same confined
descriptor walk as workspace operations.

`restore-native` verifies the completed snapshot against recorded size, digest
and native identity before touching the selected file. It retains the interrupted
file separately, then uses atomic replacement with file/parent synchronization.
A retry first checks whether the completed bytes are already restored. It neither
overwrites the retained interrupted copy nor treats a changed snapshot as valid.

`harness-journal-replay` reads a retired partition under `/harness-bridge` during
maintenance recovery. It refuses a live producer, missing files and symlinked
path components. It copies the database and committed WAL into a private
snapshot bounded to 512 MiB, then opens that copy read-only. The original journal,
ACK position and command ledger remain unchanged. Its protocol accepts only
scoped synchronization requests; it cannot execute old commands or collect the
source partition. The template manifest includes the reader and its library.
