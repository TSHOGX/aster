# Aster

Aster is a Project workbench for concurrent Agent conversations, retained
Terminals and shared workspace files. A Project binds one Khora sandbox and an
immutable Volume plan with workspace/runtime roles, optional owned data and shared
read-only publications. Sessions and Terminals are independent children of that
Project. Closing a browser view leaves its work running.

Source: [TSHOGX/aster](https://github.com/TSHOGX/aster).
Enterprise deployments can set their own [frontend display name](docs/configuration.md#frontend-display-name).
Packages, protocols and operational identifiers use functional, brand-neutral names.

The Go application owns authentication, the product database, HTTP/SSE/WebSocket,
runtime workers and the browser edge. Independent Next.js builds serve the workbench and `/admin`, with separate assets.
Administrators manage database accounts, groups, permission policies, per-user
resource entitlements, runtime defaults, network settings and tracked suspension.
Operator-registered compatible runtime and built-in Volume versions remain pinned
by each Project's saved plan; default changes affect future first allocations.

Native Pi runs under the Go Agent guardian inside the Project. Khora is the only
execution backend, accessed through its pinned public SDK and Connect APIs.

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r frontend/e2e/requirements.txt
npm --prefix frontend ci
npm --prefix admin-frontend ci
npm ci --ignore-scripts --prefix orchestrator/internal/runtimerelease
cd frontend
npx --no-install playwright install chromium
cd ..
scripts/check.sh all
scripts/check.sh race
```

Future work can be created in conversation with native scheduling tools or managed
on the Scheduled page. One-shot tasks, recurrence, and finite event checks use
the same durable admission path; see [scheduling](docs/scheduling.md).

Start with [configuration](docs/configuration.md) and [operations](docs/operations.md).
See the [architecture](docs/architecture.md), [development guide](docs/development.md),
[verification gates](docs/verification.md), [delivery process](docs/delivery.md)
and [documentation index](docs/README.md). Host toolchains are declared in
`orchestrator/go.mod` and `.node-version`.
Image assembly and real runtime verification require a disposable Linux builder
with the [documented substrate](docs/sandbox-image.md).

The supported deployment has one authoritative application owner. Only the exact current database schema can reopen; fresh databases initialize
atomically and historical schemas are refused without migration. Khora is the sole execution backend.
Keep credentials, deployment inventories, databases and operational evidence out
of source control; see the [publication boundary](docs/delivery/privacy.md).
