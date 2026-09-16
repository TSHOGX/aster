# Orchestrator

Go control plane for session state, turn dispatch, sandbox resources, events,
sharing and scheduling. The optional memory subsystem uses a separate database.

Build from the repository root:

```bash
go -C orchestrator build -o bin/orchestrator ./cmd/orchestrator
go -C orchestrator test ./...
go -C orchestrator vet ./...
```

Run with `--config /absolute/private/harness.yaml`. For managed deployments,
use release binaries, explicit migrations and the deployment CLI rather than
compiling on production hosts.

- [Architecture](../docs/architecture.md)
- [Control plane](../docs/control-plane.md)
- [Configuration](../docs/configuration.md)
- [Database operations](../docs/database.md)
- [HTTP API](../docs/api.md)
- [Development and testing](../docs/development.md)
