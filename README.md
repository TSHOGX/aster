# BatteryGPT Khora development branch

A Go product control plane and Next.js workbench for retained agent sessions on
Khora. Conversations, workspaces, durable turn recovery and Pi native history
survive disposable processes. Khora is the only execution backend.

This branch intentionally breaks production compatibility. Memory and Evolve are
removed; shared assets are manually maintained, versioned and read-only. Use fresh,
isolated development databases. Production migration and deployment acceptance are
a separate project.

```bash
go -C orchestrator build ./...
go -C orchestrator vet ./...
go -C orchestrator test ./...
python3 -m unittest discover -s sandbox-image/tests -p 'test_*.py'
cd frontend
npm ci
npm run lint
npm run typecheck
npm test
npm run build
```

See [development](docs/development.md), [architecture](docs/architecture.md),
[verification](docs/verification.md), [recorded evidence](docs/development-evidence.md), [scope](docs/development-scope.md) and the
[documentation index](docs/README.md). Fake-backed tests do not replace isolated
real Khora integration. No production release or deployment readiness is claimed.

Keep credentials, real endpoint inventories, databases and raw operational logs
outside version control. `private/` is an ignored local planning/evidence area;
see the [public/private boundary](docs/delivery/privacy.md).
