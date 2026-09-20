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
