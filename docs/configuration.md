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
| `HARNESS_KB_PROMPTS_PATH` | Optional private overlay of embedded KB evolution prompts |
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

KB evolution prompts live in `orchestrator/internal/memory/prompts.yaml` and are
embedded into the binary. See [KB evolution prompts](#kb-evolution-prompts-optional-override).

External evaluation/evolution integration requires explicit `EVOLUTION_*`
variables. It has no checked-in host path and is disabled when configuration is
absent. Its implementation boundary and allowlist behavior are documented in
[memory and knowledge](agents-kb.md).

## KB evolution prompts (optional override)

Every LLM system prompt in the extraction/curation loop — the three harvest
lenses and each graph node — lives in `orchestrator/internal/memory/prompts.yaml`,
embedded into the binary at build time. Those committed defaults are complete
and generic: with no override configured the loop runs entirely on them, and a
release tarball (binary only, no `config/`) needs nothing extra.

A deployment overrides individual prompts by pointing `HARNESS_KB_PROMPTS_PATH`
at a YAML file of the same keys. Per-deployment material belongs there — a real
scope taxonomy for the B0 blocking-key node, house wording, a tightened PII
clause — and must not enter the tracked repository. Keys: `grounding`, `facts`,
`failure`, `skill`, `reflect`, `block_key`, `cluster`, `reduce`, `kb_dedup`,
`verify`, `memory_curate`, `skill_critic`, `critic`.

```yaml
# kb-prompts.local.yaml (gitignored via **/*.local.yaml)
block_key: |-
  ...deployment-specific scope taxonomy used as examples...
```

Semantics match `HARNESS_SCHEMA_BASELINES_PATH` in `internal/schemastate`:

- Present keys replace the whole prompt body; absent keys keep the embedded one.
- `grounding` is prepended to every prompt except `reflect`, so overriding it
  cascades into the other eleven.
- Env var unset → embedded prompts only. Not an error, and no default path is
  guessed: an external job wrapper may run with cwd `/`, where a relative default
  would resolve elsewhere and silently fall back.
- Env var set but unusable (missing file, malformed YAML, unknown key, empty
  body) → startup fails. A typo has to be loud here, or it resurfaces later as
  degraded consolidation that looks like a model problem.

There is no `.example.yaml` twin: unlike `memory-llm.example.yaml`, this file is
a partial overlay, and `prompts.yaml` itself is the readable full schema. Which
prompt text actually ran is auditable per call via `agent_calls.prompt_sha`.

If a host takes a new binary without its override file, the loop silently falls
back to the generic `block_key` example and consolidates scopes less well (a
quality regression, not a correctness bug). Deploy the override with the binary.
The B0 log line (`N raw scopes → M canonical`) is the observable signal.
External job wrappers should set `HARNESS_KB_PROMPTS_PATH` through the operator
env file (`HARNESS_JOB_ENV`), not by hardcoding a host path in the wrapper.
