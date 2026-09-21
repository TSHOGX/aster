# Go Project application

The application owns Project state, authenticated HTTP/SSE/WebSocket, retained
Agent/Terminal workers, Files, sharing, schedules and the same-origin browser edge.
It uses the pinned public Khora SDK/Connect APIs and one authoritative product
SQLite database.

```sh
go build -o bin/orchestrator ./cmd/orchestrator
go test -race ./...
go vet ./...
bin/orchestrator serve --config /etc/batterygpt/application.json
```

Guest commands are `agent-runner`, `workspace-files`, `runtime-cleanup` and
`runtime-verify`. `runtime-release` builds the immutable image manifest;
`runtime-fixture` is for isolated tests and is never packaged in a release image.

See [architecture](../docs/architecture.md), [configuration](../docs/configuration.md),
[database](../docs/database.md), [API](../docs/api.md) and
[development](../docs/development.md).
