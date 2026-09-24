# BatteryGPT

BatteryGPT is a Project workbench for concurrent Agent conversations, retained
Terminals and shared workspace files. A Project owns one Khora sandbox, one
workspace volume and one runtime volume. Sessions and Terminals are independent
children of that Project. Closing a browser view leaves its work running.

The Go application owns authentication, the product database, HTTP/SSE/WebSocket,
runtime workers and the browser edge. Next.js serves the workbench and assets.
Native Pi runs under the Go Agent guardian inside the Project. Khora is the only
execution backend, accessed through its pinned public SDK and Connect APIs.

```sh
go -C orchestrator test -race ./...
go -C orchestrator vet ./...
cd frontend
npm ci
npm run typecheck
npm run lint
npm test
npm run build
```

Start with [configuration](docs/configuration.md) and [operations](docs/operations.md).
See the [architecture](docs/architecture.md), [development guide](docs/development.md),
[verification gates](docs/verification.md) and [documentation index](docs/README.md).
Image assembly and real runtime verification require a disposable Linux builder
with the [documented substrate](docs/sandbox-image.md).

The supported deployment has one authoritative application owner. Only the exact current database schema can reopen; fresh databases initialize
atomically and historical schemas are refused without migration. Khora is the sole execution backend.
Keep credentials, deployment inventories, databases and operational evidence out
of source control; see the [publication boundary](docs/delivery/privacy.md).
