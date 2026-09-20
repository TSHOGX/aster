# Configuration

`orchestrator --config PATH serve` reads explicit YAML independently of the current
working directory. `HARNESS_CONFIG_PATH` supplies the default explicit path. Unknown
YAML fields are rejected, including removed runtime/network/checkpoint settings.
[The product example](../config/harness.yaml) controls the run directory, session
capacity, event retention, scheduled work and optional turn continuation.

`HARNESS_KHORA_DEPLOYMENT` is required. The strict JSON loader in
`internal/retainedapp/deployment.go` validates engine connection identity, pinned
launch plans, authorization socket, gateway peers, prepared-content root and bounded
service intervals. Engine and gateway credentials remain outside guest configuration.
No environment variable enables an embedded runtime or a second backend.

A plan names authored content by directory, not by engine volume: `assets` is a
shared context/skills tree mounted read-only at `/root/.agents`, and `config` is the
immutable agent configuration mounted read-only at `/harness-config`, which Pi
requires. `content_bytes` caps each published content volume. The loader reads each
tree and computes its revision digest, so a document cannot assert an identity its
bytes do not have. Startup publishes and verifies every revision before the server
accepts work, and a launch mounts only a revision this engine store has published.

Publishing newer content is a new revision with its own volume; a published volume
is never rewritten. A retained session adopts it at its next generation through a
fenced configure while the environment is paused.

The product database defaults to `.harness-local/development/control.db` beneath the
repository. `HARNESS_DB_PATH` selects another isolated development file. Parent
paths must be writable. `HARNESS_RUN_DIR` selects the owner-lock/heartbeat directory.
`HARNESS_ORCHESTRATOR_ADDR` selects the HTTP listen address (default `:8090`); use a
different endpoint for the Khora engine. Session storage is retained indefinitely.

Authentication settings include `HARNESS_AUTH_USERS_PATH`, `HARNESS_LAB_PASSWORD`,
`HARNESS_AUTH_SESSION_TTL`, `HARNESS_COOKIE_NAME`, `HARNESS_COOKIE_INSECURE`,
`HARNESS_TRUST_FORWARDED_PROTO` and `HARNESS_TRUST_PROXY_HEADERS`. The auth user file
holds password hashes and roles; use the `user` command to manage it.

Upload/edit bounds use `HARNESS_MAX_UPLOAD_BYTES`, `HARNESS_MAX_UPLOAD_FILES` and
`HARNESS_MAX_EDIT_BYTES`. Public snapshot files use `HARNESS_SHARE_BLOB_ROOT`,
`HARNESS_MAX_SHARE_LINK_TTL`, `HARNESS_MAX_SHARE_LINK_BYTES` and
`HARNESS_MAX_SHARE_LINK_FILES`. Optional title generation reads
`HARNESS_TITLE_LLM_PATH` (default `config/title-llm.yaml`).

The separate native-capture command requires `--control-db`, `--trajectory-root`
and `--engine-endpoint`, with optional bounded polling and batch timeout. It opens
an existing current product database and holds a canonical singleton capture lock.
There are no Memory, Evolve, host workspace, checkpoint or legacy proxy settings.

Networking is configured once in the deployment's optional `connectivity` block;
launch plans do not accept independent network declarations. See the
[gateway contract](gateway-contract.md) for endpoint, direct-rule, resolver and
next-hop fields. An empty policy runs locally with zero gateways. Model proxy
credentials and resource allowlists live in the proxy's separate configuration.
The current database schema stores only generic route/grant authority; earlier
schemas are rejected before writable access.


Versioned examples:

- [Local shell deployment](../config/khora-local.example.json): no gateways or network access.
- [Pi and shell deployment](../config/khora-model.example.json): managed model endpoint, local shell.
- [Separate model proxy](../config/model-gateway.example.json): upstream URL, model allowlist and credential file.

Copy examples to private files and replace engine store ID, exported manifest path
and digest, template name, network addresses and peer UID/group IDs. Paths resolve
beside each document. The examples use application UID 1000 and gateway UID/group
1001 as placeholders. The engine's second-hop UID must match the actual gateway
process; create shared socket directories with matching access. Start the proxy
before the application (its control listener does not require application
readiness). Keep its credential file mode 0600 and owned by the gateway UID.
