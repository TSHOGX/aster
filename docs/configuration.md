# Configuration

Application configuration is a strict YAML document under `harness:`. Unknown
keys fail parsing. The full schema/defaults/validation live in
`orchestrator/internal/config`; public examples intentionally contain no machine
addresses, service mappings or private knowledge.

## Resolution

- `orchestrator --config PATH` takes precedence over `HARNESS_CONFIG_PATH`.
- An explicit missing/invalid YAML file fails; it does not silently use defaults.
- Without an explicit path, development uses `config/harness.yaml` relative to
  the working tree. Use an explicit path outside source for managed services.
- Supported `HARNESS_*` overrides apply after YAML loading.
- Auth/title/memory auxiliary YAML defaults beside the selected application
  configuration; their explicit path overrides remain available.

Do not put production secrets into a committed YAML file. Copy templates to
private configuration and manage permissions there. `HARNESS_MANAGED=1` requires
a versioned binary, a nonempty auth file and an already migrated database pair.

## Important environment settings

| Setting | Purpose |
|---|---|
| `HARNESS_CONFIG_PATH` | Application YAML |
| `HARNESS_MANAGED` | Managed startup checks (`1`) |
| `HARNESS_ORCHESTRATOR_ADDR` | HTTP bind address |
| `HARNESS_RUN_DIR` | Owner lock, control/bridge state and proxy socket root |
| `HARNESS_DB_PATH` | Control database |
| `HARNESS_MEMORY_DB_PATH` | Separate memory database (defaults beside control) |
| `HARNESS_SCHEMA_BASELINES_PATH` | Optional trusted private schema catalog |
| `HARNESS_SESSIONS_ROOT` | Session workspaces |
| `HARNESS_AGENT_HOMES_ROOT` | Persistent driver homes |
| `HARNESS_CHECKPOINTS_ROOT` | Checkpoint storage |
| `HARNESS_BUNDLE_ROOT` | Runtime bundles |
| `RUNSC_ROOT` | runsc state root |
| `HARNESS_ROOTFS_PATH` | Immutable sandbox rootfs |
| `HARNESS_AGENT_IMAGE_MANIFEST_PATH` | Explicit image manifest override |
| `HARNESS_RESOLV_CONF` | Resolver file mounted read-only into managed sandboxes |
| `HARNESS_AGENTS_KB_PATH` | Snapshot knowledge base root |
| `HARNESS_TRAJECTORY_ROOT` | Archived native traces |
| `HARNESS_SHARE_BLOB_ROOT` | Public-share file snapshot storage |
| `HARNESS_AUTH_USERS_PATH` | Credential YAML |
| `HARNESS_TITLE_LLM_PATH` | Optional title model configuration |
| `HARNESS_MEMORY_LLM_PATH` | Optional extraction model configuration |
| `HARNESS_DRAIN_FILE` | Local deployment drain marker |
| `HARNESS_BACKGROUND_HOLD_FILE` | Hold automatic jobs during internal smoke |
| `HARNESS_API_BASE_URL` / `ORCHESTRATOR_URL` | Frontend server-side backend target |
| `PORT` / `HOSTNAME` | Frontend listener |
| `HARNESS_DEV_ORIGINS` | Development browser origins |

The deployment CLI reads a separate target JSON through its own `--config` flag.
Its environment mapping is forwarded to application processes; deployment paths
and services are explicit target input, not an instance-name convention.

## Core YAML areas

| Area | Controls |
|---|---|
| `agents`, `model_profiles`, `runtime_providers` | Enabled drivers and product-mode resolution |
| `session_retention`, `max_sessions` | Session expiry policy and live sandbox admission |
| `model_proxy` | Host bind address and stable sandbox-facing alias |
| `sandbox_identity`, `proxy_service_identity` | Sandbox uid/gid and correlation peer identity |
| `network` | CIDR allocation, egress destinations and DNS policy |
| `data_proxies` | Optional host gateway aliases and upstream metadata |
| `events` | Event retention and output batching |
| `probe` | Runtime start/probe retries and accepted responses |
| `bridge` | Lease, heartbeat, poll, reconnect and proxy grace intervals |
| `scheduler` | Core scheduled tasks, worker limits, retries, timeout and misfires |
| `runtime` | Command timeout and sandbox memory/PID limits |
| `maintenance` | Stage deadline and cold-fallback interval |
| `checkpoint` | Automatic idle release and monitor timing |
| `reaper` | Failed-generation and checkpoint-image retention |

Runtime command deadlines must fit within maintenance stage deadlines. Proxy lease
grace must stay below sandbox heartbeat stall grace. A CIDR pool must have usable
per-generation subnets. Isolated roots must not overlap or resolve through unsafe
paths. Validation errors should be corrected in configuration, not bypassed.

## Credentials and egress

The model/data gateways hold real upstream credentials. Sandboxes use stable
aliases and scoped/dummy credentials. Configure actual gateway processes and
network policy separately; enabling a `data_proxies` entry does not install a
working gateway.

`network.egress.allow_all` explicitly permits broad outbound access; it is not an
exfiltration boundary. Restricted egress allows only configured destinations.
The checked-in development example permits outbound access while keeping data
gateways disabled. It contains no real upstream host or
database names. Enabling access is an operator decision.

Authentication uses a private `users:` file with username, password_hash and
optional role (`user` or `admin`). The legacy `lab` identifier is an application
compatibility identity, not a host name. Open mode is only for isolated unmanaged
development. Use TLS, Secure cookies and trusted proxy-header configuration for
user-facing deployments.

## Optional memory/evolution configuration

The extraction model and agentic pipeline settings are separate from the core
scheduler. Missing LLM credentials leave extraction disabled. Use
`HARNESS_MEMORY_AGENTIC`, `HARNESS_MEMORY_AUTO_PUBLISH` and supported concurrency
settings only after selecting a single writer/scheduling authority.

External evaluation/evolution integration requires explicit `EVOLUTION_*`
variables. It has no checked-in host path and is disabled when configuration is
absent. Its implementation boundary and allowlist behavior are documented in
[memory and knowledge](agents-kb.md).

## Retained Khora daemon

Set `HARNESS_KHORA_DEPLOYMENT` to a version-1 JSON deployment file to select the
retained backend in `orchestrator serve`. Missing or invalid files are fatal; there
is no fallback to the embedded runtime. The application YAML still configures
HTTP/authentication, sharing, scheduler and product retention. Session retention
must be zero. Startup rejects any database containing sessions pinned to the
legacy backend; this mode does not migrate them implicitly.

The JSON document requires these fields:

| Field | Value |
|---|---|
| `version` | `1` |
| `backend_name` | Stable application name for the engine |
| `engine.endpoint`, `engine.store_id` | Engine origin and explicit durable store pin |
| `manifest`, `manifest_digest` | Exported template manifest file and canonical `sha256:` digest |
| `plans` | Driver IDs mapped to immutable launch plans |
| `gateways` | One to sixteen gateway control endpoints |
| `authorization` | Application authorization socket and kernel peer/group configuration |
| `prepared_root` | Directory for prepared upload content |
| `max_active`, `inventory_limit` | Positive limits; inventory maximum is 10000 |
| `lease_ttl`, `poll_interval`, `event_interval`, `action_timeout` | Positive duration strings |
| `capture_window`, `inventory_interval`, `idle_interval`, `idle_threshold` | Positive duration strings |

Each plan requires `template`, `cpu_millis`, `memory_bytes`, `disk_bytes`,
`workspace_bytes` and `journal_bytes`. Resource values must be positive. Optional
`config_volume` mounts an existing immutable volume at `/harness-config`;
`assets_volume` mounts one at `/root/.agents`. Pi requires a config volume. Optional
`network` uses the application sandbox vocabulary (`Allow` tuples and `Via`
routes); `environment` is limited to the launch-plan allowlist. The manifest
validator checks driver, bridge, engine and installed-component compatibility
before any backend connection. Spec digests are computed from the validated
plans, not supplied in JSON.

Each gateway entry has `id`, `protocol`, `socket` and explicit `peer_uid`.
`authorization` has `socket`, explicit `peer_uid` and `group`. All configured
gateways must run under that authorization peer UID. IDs and socket paths must be
unique, including the application's authorization socket. See the
[gateway control contract](gateway-contract.md) for handshake and drain semantics.

Relative paths resolve beside the deployment file. Plain HTTP requires a loopback
IP literal. HTTPS requires `engine.ca_file`, `engine.client_cert` and
`engine.client_key`; the client validates the server and presents its certificate.
Engine origins cannot contain credentials, paths, queries or fragments. Redirects
and HTTP proxy environment settings are disabled for engine/control connections.
Never point `manifest` at an engine-owned mutable template directory; use the
exported release artifact.

This mode runs retained execution, event reconciliation, workspace inventory,
product maintenance, scheduler and idle pause services. It serves the new Unix
request-authorization contract and uses remote gateway drain acknowledgments. It
does not construct the embedded runtime, local artifact watcher, checkpoint
maintenance or source-IP proxy-correlation listener. Shutdown detaches application
connections and keeps retained sandboxes available for adoption/resume.

An actual daemon subprocess acceptance test covers HTTP upload/turns, automatic
pause and restart/resume without creating host runtime directories. Existing
config/asset volumes must already be published. Production model/Doris/OBS route
allocation, gateway protocol integration, deletion parity and migration remain
separate work; selecting this mode does not claim those cutover gates are met.
