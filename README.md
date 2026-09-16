# Agent Runtime Platform

A Go control plane and Next.js workbench for persistent AI agent sessions running
inside gVisor sandboxes. Each session keeps its conversation, workspace and agent
history independently of the sandbox processes used to execute its turns.

## Deploy a release

Download a versioned application bundle and sandbox from
[GitHub Releases](https://github.com/TSHOGX/agent-runtime-platform/releases).
Releases include the orchestrator, a standalone frontend with Node, the deployment
CLI, sandbox rootfs, a manifest and checksums. Production hosts do not build source
and do not need Git, Go, npm, Docker or a CI runner.

Start with the [deployment guide](docs/delivery/operations.md). Linux amd64 on
Ubuntu 22.04/24.04 with systemd is the current packaged deployment target. The
host supplies runsc, networking tools, nginx and private configuration.

## Develop locally

Development requires Linux for the Go runtime paths. Follow
[development setup](docs/development.md) using a private data root and explicit
configuration; never reuse a production database or runsc root.

```bash
# From the repository root; use the pinned CI toolchain for release checks.
go -C orchestrator build ./...
go -C orchestrator test ./...

cd frontend
npm ci
npm run lint
npm run typecheck
npm test
npm run build
```

Some backend tests require root ownership, as in CI. Actual sandbox and model
smokes also require runsc and a configured external model proxy. See
[verification](docs/verification.md) for the distinction between unit tests,
artifact checks and deployment-local acceptance.

## System boundaries

- **Core runtime:** durable sessions and turns, bridge protocol, sandbox resource
  lifecycle, model request authorization, events, files, sharing and scheduling.
- **Workbench:** same-origin API proxy, session UI, runtime projections and files.
- **Optional memory subsystem:** trajectory capture, separate memory database,
  knowledge snapshots, extraction and approval. Its jobs and data lifecycle are
  described separately in [memory and knowledge](docs/agents-kb.md).
- **External integrations:** production model/data gateways and the optional
  evolution/evaluation runner are separately deployed dependencies. Their source,
  data and credentials are not included in this repository.

The [architecture](docs/architecture.md) and [documentation index](docs/README.md)
describe implemented behavior. Future work belongs in
[GitHub Issues](https://github.com/TSHOGX/agent-runtime-platform/issues), including
[the existing core roadmap](https://github.com/TSHOGX/agent-runtime-platform/issues/3).
Release announcements belong in release notes, not an in-tree archive.

## Private deployment information

Keep machine inventories, real endpoint addresses, credentials, databases,
operational logs and deployment-local schema catalogs outside version control.
`private/` is an ignored local handoff area; it is never a public configuration
source or a release input. See [privacy and public-source policy](docs/delivery/privacy.md).
