# Configuration Reference

> Primary config is `config/harness.yaml` under a strict `harness:` schema; env vars
> override selected values. Loader: `orchestrator/internal/config/config.go`
> (`KnownFields(true)` — unknown keys fail startup; only the `harness:` document is
> decoded). Login users are in `config/auth-users.yaml`, and the optional session-title
> LLM in `config/title-llm.yaml` — both are gitignored secret files loaded the same
> way (missing file = feature simply off). See [security.md](./security.md).
> Everything else is read once at startup, but `auth-users.yaml` is the exception:
> it is reloaded whenever it changes, so `harness-ctl user add|passwd|rm` needs no
> restart. See [operations.md](./operations.md) §3.

## `config/harness.yaml` (checked-in lab profile)

```yaml
harness:
  default_agent: pi
  agents:
    sh:          { enabled: true, driver_id: sh, runtime_provider: local_runsc }
    pi:          { enabled: true, driver_id: pi, model_profile: anthropic_default, runtime_provider: local_runsc, disable_nonessential_traffic: true }
  model_profiles:
    anthropic_default: { enabled: true, provider: anthropic_messages, model: sonnet, proxy_ref: model_proxy }
  runtime_providers:
    local_runsc: { enabled: true, provider_id: local_runsc, profile_id: local_runsc_default }

  run_dir: /var/lib/harness-publish/run
  session_retention: 0s
  max_sessions: 30
  model_proxy:
    bind_url: http://0.0.0.0:8083
    sandbox_base_url: http://harness-model-proxy.internal:8083
  sandbox_identity: { uid: 65534, gid: 65534, supplemental_gids: [] }

  network:
    cidr_pool: 10.201.0.0/16
    egress:
      doris_fe_hosts: [172.16.0.138]
      doris_be_hosts: [172.16.0.138]
      doris_ports: [9030]
      dns_policy: hostnames_only
      allow_all: true          # open sandbox egress to the whole internet

  events:      { retention_window: 24h, retention_rows: 1000000, emit_output_batch_max_rows: 64, emit_output_batch_max_age: 100ms }
  probe:       { accept_status: { get_healthz: [200], post_v1_messages: { unauthorized: [401], malformed_authenticated: [400] } },
                 pre_start_attempts: 3, pre_start_interval: 500ms, post_start_attempts: 5, post_start_interval: 1s }
  bridge:      { lease_ttl: 60s, heartbeat_interval: 30s, poll_interval: 5ms, ack_started_grace: 90s, reconnect_grace: 30s }
  checkpoint:  { auto_enabled: true, idle_threshold: 2h, monitor_interval: 5m }
  reaper:      { failed_retention: 10m, checkpoint_image_retention: 720h }
```

## Key knobs

| Key | Meaning |
|---|---|
| `default_agent` | driver the product `agent` mode resolves to (checked-in: `pi`) |
| `agents.<id>` | per-driver deployment entry: `enabled`, `driver_id`, `model_profile`, `runtime_provider`, `disable_nonessential_traffic` |
| `model_profiles.<id>` | `enabled`, `provider`, `model`, `proxy_ref` (must be `model_proxy` for model-access drivers) |
| `runtime_providers.<id>` | `enabled`, `provider_id`, `profile_id` (only `local_runsc` ships) |
| `run_dir` | root for control/bridge/logs/proxy socket |
| `session_retention` | `0s` = sessions/history/workspace never auto-expire |
| `max_sessions` | soft ceiling on non-terminal sessions (independent of `/30` pool) |
| `model_proxy.bind_url` | host listener for the proxy (host must be unspecified, port explicit) |
| `model_proxy.sandbox_base_url` | sandbox-visible alias; its port must match `bind_url` |
| `sandbox_identity` | non-root uid/gid the sandbox runs as (must be `>0`; default `65534`) |
| `network.cidr_pool` | IPv4 `/≤30` pool carved into per-generation `/30`s |
| `network.egress` | Doris FE/BE hosts + ports; `dns_policy` = `off`\|`hostnames_only`\|`always`; `allow_all` (see below) |
| `agents.<id>.disable_nonessential_traffic` | when set, disables the driver's non-essential outbound (e.g. Pi telemetry/version checks) regardless of egress mode |
| `events` | durable-event retention + `emit_output` batching |
| `probe` | accepted proxy statuses + pre/post-start attempt counts and intervals |
| `bridge` | lease TTL, heartbeat/poll intervals, ack-started grace, reconnect grace |
| `checkpoint.auto_enabled` | enable automatic **idle release** (see below) |
| `checkpoint.idle_threshold` | idle time before release (checked-in `2h`) |
| `reaper.failed_retention` | how long failed generations linger before physical cleanup |
| `reaper.checkpoint_image_retention` | how long a `checkpointed` session keeps its slot |

### Egress semantics

`network.egress.allow_all: true` (the checked-in default) opens sandbox egress to the
entire public internet — agents can pip install, reach GitHub/PyPI, and search the
web. It emits an "all" sentinel rule that flips the sandbox output chain to
policy-accept. This **removes the default-deny exfiltration boundary**; see the
tradeoff in [security.md](./security.md). Set `allow_all: false` to restore
default-drop egress where only Doris (`doris_*` hosts/ports) and the model-proxy alias
are reachable, with `dns_policy` gating name resolution. The policy is baked per
generation at sandbox creation, so only newly created sandboxes pick up a change.

### Idle release semantics

`checkpoint.auto_enabled: true` enables automatic **idle release**, not `runsc`
checkpoint images: after `idle_threshold` the monitor tears down the idle sandbox to
free the compute slot, the session becomes `checkpointed` (logical "released"), and
the next message cold-starts a fresh generation from the retained workspace +
agent-home. See [sandbox-runtime.md](./sandbox-runtime.md).

## `config/title-llm.yaml` (optional, gitignored)

Configures the LLM that generates session titles from the first message. It is a
**separate secret file** (like `auth-users.yaml`) so the API key never enters git;
`config/title-llm.example.yaml` is the checked-in template. When the file is absent
or `api_key` is empty the feature is off and sessions keep their placeholder title
(the truncated first message). This is a plain OpenAI-compatible endpoint, wholly
independent of the model proxy and the sandbox credential boundary — the
orchestrator calls it directly from the host.

```yaml
enabled: true
base_url: https://api.modelarts-maas.com/openai/v1
api_key: "..."          # host-only secret; never committed
model: glm-5.2
```

Loaded via `HARNESS_TITLE_LLM_PATH` (default `<repo>/config/title-llm.yaml`).

## Environment variables (`config.go`)

| Env var | Default | Purpose |
|---|---|---|
| `HARNESS_ORCHESTRATOR_ADDR` | `:8090` | HTTP bind |
| `HARNESS_LOG_LEVEL` | info | `debug` raises log level |
| `HARNESS_LAB_PASSWORD` | (empty) | shared-secret login fallback |
| `HARNESS_COOKIE_NAME` | `harness_auth` | auth cookie name (`__Host-` prefixed over HTTPS) |
| `HARNESS_COOKIE_INSECURE` | false | set `1` to allow non-Secure cookie (local http only) |
| `HARNESS_TRUST_FORWARDED_PROTO` | true | trust `X-Forwarded-Proto` to detect HTTPS |
| `HARNESS_TRUST_PROXY_HEADERS` | true | trust reverse-proxy client-IP headers for rate limiting: prefer nginx-overwritten `X-Real-IP`, fall back to the rightmost `X-Forwarded-For`; set `0` when exposing orchestrator directly |
| `HARNESS_AUTH_USERS_PATH` | `<repo>/config/auth-users.yaml` | login users file; re-read on change (no restart) |
| `HARNESS_TITLE_LLM_PATH` | `<repo>/config/title-llm.yaml` | session-title LLM secret file (optional) |
| `HARNESS_AUTH_SESSION_TTL` | 24h | login session TTL |
| `HARNESS_MAX_SHARE_LINK_TTL` | 720h (30d) | ceiling on a public share link's lifetime; must be positive, so a link can never be permanent |
| `HARNESS_SHARE_BLOB_ROOT` | `share-blobs` beside `HARNESS_DB_PATH` | content-addressed snapshots served by public links |
| `HARNESS_MAX_SHARE_LINK_BYTES` | `8 MiB` | total bytes one public link may snapshot (all files combined) |
| `HARNESS_MAX_SHARE_LINK_FILES` | 25 | how many files one public link may snapshot |
| `HARNESS_SESSION_RETENTION` | config `0s` | session expiry (0 = never); invalid values fail startup |
| `HARNESS_SESSION_TTL` | — | **obsolete; fatal if set** |
| `HARNESS_DEFAULT_AGENT` | config `pi` | product Agent driver |
| `HARNESS_MAX_SESSIONS` | 30 | soft session ceiling |
| `HARNESS_MAX_UPLOAD_BYTES` | `100 MiB` | cap on a single workspace upload request |
| `HARNESS_MAX_UPLOAD_FILES` | 2000 | cap on files per upload request |
| `HARNESS_MAX_EDIT_BYTES` | `1 MiB` | cap on a single online-edit body |
| `HARNESS_AUTO_CHECKPOINT_ENABLED` | config | override idle-release policy for new sessions |
| `HARNESS_SESSIONS_ROOT` | `/var/lib/harness/sessions` | session workspaces root |
| `HARNESS_AGENT_HOMES_ROOT` | `/var/lib/harness/agent-homes` | agent-home root |
| `HARNESS_CHECKPOINTS_ROOT` | `/var/lib/harness/checkpoints` | checkpoint images root |
| `HARNESS_BUNDLE_ROOT` | `<repo>/bundle/out` | OCI bundle root |
| `HARNESS_ROOTFS_PATH` | `<repo>/sandbox-image/rootfs` | rootfs template |
| `HARNESS_AGENT_IMAGE_MANIFEST_PATH` | `<rootfs>/etc/harness-image/agents.json` | driver manifest |
| `HARNESS_DB_PATH` | `/var/lib/harness/state/orchestrator.db` | SQLite path |
| `HARNESS_REPO_ROOT` | detected | repo root |
| `RUNSC_ROOT` | `/var/lib/harness/runsc` | runsc state root |

Hardcoded in the loaded config: `RunscNetwork=sandbox`, `RunscOverlay2=none`,
`RequireAgentManifest=true`. `IsolationRoots`/`ValidateIsolationRoots` require every
data root to be absolute, non-`/`, symlink-resolved, and mutually disjoint.

## Rejected keys (fail startup)

Strict decoding rejects these; set them and the orchestrator refuses to start.

- `HARNESS_SESSION_TTL` / `harness.session_ttl` — use `session_retention`.
- Top-level `runtime:` / `claude:` documents and `harness.secrets.*` — model-proxy
  settings live only under `harness.model_proxy`, and credentials stay host-side.

## Ports

| Component | Checked-in default | Publish deployment |
|---|---|---|
| Orchestrator HTTP | `:8090` | `:9090` |
| Frontend | `PORT=8000` | `127.0.0.1:9001` (behind nginx TLS) |
| Model proxy | `:8083` | `:8083` |

The publish deployment overrides are set in the systemd units and documented in
[operations.md](./operations.md). Two orchestrators cannot share the default
`:8090` on one host — hence the publish `:9090`.
