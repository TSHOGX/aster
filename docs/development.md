# Development

Use a fresh product database and an isolated Khora engine store. The application
validates existing databases against its exact current schema; it has no migration
path for production or earlier branch databases. See [scope](development-scope.md).

## Prerequisites and source checks

Use Go with the toolchain in [go.mod](../orchestrator/go.mod), Node.js 22 or newer
(the CI version is pinned in [.github/workflows/ci.yml](../.github/workflows/ci.yml)),
and npm with the committed lockfile. Access to the private Khora module is needed
for an empty Go cache; [fetch-go-dependencies.sh](../scripts/fetch-go-dependencies.sh)
describes the CI credential handoff. No sibling checkout is required.

[Verification](verification.md) separates ordinary source checks, browser fixtures
and real-engine journeys. Ordinary Go and frontend checks need no running engine.
For a disposable integration environment, build a [template](sandbox-image.md) and
use the [isolated harness](../dev/README.md). That harness starts its own engine,
daemon and synthetic upstreams and destroys its test substrate when finished.

## Interactive local shell setup

An interactive daemon needs a separately running pinned engine, its store ID,
a registered template and its exported manifest. The verification harness is a
finite test runner, not a persistent development engine launcher. Keep interactive
engine storage and test storage separate.

1. Copy [harness.yaml](../config/harness.yaml) and
   [khora-local.example.json](../config/khora-local.example.json) into a private
   configuration directory. Set the deployment's engine endpoint/store ID,
   template name, manifest path and canonical manifest digest.
2. Resolve every deployment-relative path from the **copy's directory**. In
   particular, set `plans.sh.assets` to the authored shared-assets directory and
   `prepared_root` to private application storage. Copying an example does not
   preserve the meaning of its relative paths.
3. Choose explicit absolute paths for the product database and run directory;
   the run directory must be unique to this daemon. Keep the daemon on loopback
   for local work. Configure users using the [authentication settings](configuration.md)
   when login is needed. With no users and no lab password, requests act as `lab`.
4. From the repository root, start the daemon (replace the example paths):

   ```bash
   HARNESS_KHORA_DEPLOYMENT=/absolute/path/khora.local.json \
   HARNESS_DB_PATH=/absolute/path/state/control.db \
   HARNESS_RUN_DIR=/absolute/path/state/run \
   HARNESS_ORCHESTRATOR_ADDR=127.0.0.1:8090 \
   HARNESS_TRUST_FORWARDED_PROTO=false \
   HARNESS_TRUST_PROXY_HEADERS=false \
     go -C orchestrator run ./cmd/orchestrator \
       --config /absolute/path/harness.local.yaml serve
   ```

5. In another terminal, start the frontend:

   ```bash
   cd frontend
   npm ci
   HARNESS_API_BASE_URL=http://127.0.0.1:8090 PORT=8000 npm run dev
   ```

Open `http://localhost:8000`. Creation waits for deployment capabilities and uses
the advertised default mode; a shell-only deployment creates shell sessions.
`GET /healthz` identifies the daemon process. Use
[diagnostics](operations.md) to inspect worker, capacity and dependency state.

## Pi, gateways and native capture

Use [khora-model.example.json](../config/khora-model.example.json) for Pi and shell.
Copy the [authored Pi configuration](../config/agent-config.example/README.md) and
[model gateway configuration](../config/model-gateway.example.json), replacing all
placeholder paths, peers and network addresses. Start the gateway under the engine's
configured non-root next-hop UID before the daemon. The daemon publishes authored
content; Pi's settings select the provider/model while the gateway owns the upstream
credential. See [configuration](configuration.md) and [gateway contracts](gateway-contract.md).

Start the native capture worker separately for immutable archives:

```bash
go -C orchestrator run ./cmd/native-capture \
  --control-db /absolute/path/state/control.db \
  --trajectory-root /absolute/path/state/native-archives \
  --deployment /absolute/path/khora.local.json
```

The database must already exist. Capture reads the engine connection from the same
deployment and can request maintenance activation from the daemon when a source is
paused. Completed native state and its snapshot are durable before this asynchronous
archive step. See [recovery](recovery.md).

Stopping the daemon does not delete retained session storage. Session DELETE retires
execution and preserves its data. Reuse the same database, engine identity and
storage for restart testing. Production migration and deployment are a separate project.
