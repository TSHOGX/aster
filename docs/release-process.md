# Versioning, Requirements & Release Process

How this repo tracks proposed work, verifies changes, and cuts releases.
Established 2026-07-09 (first tag `v0.1.0`). If something here and the actual
CI/workflow files disagree, the workflow files
(`.github/workflows/ci.yml`, `.github/workflows/release.yml`) are authoritative
— update this doc to match, not the other way around.

## Branching

Single development branch: `main`. There is no `develop` branch and no
release branches — the project is small enough that the overhead isn't
worth it yet; revisit if/when there are multiple concurrent contributors
needing longer-lived integration branches.

Parallel feature work uses `git worktree` off `main`, one worktree per
feature, merged back via PR. See [parallel-development.md](./parallel-development.md)
for the exact commands.

## Requirements: lightweight RFCs

New features, non-trivial design questions, and investigation writeups go in
`docs/proposals/` as plain Markdown files — no external issue tracker. Each
file leads with `Status` / `Date` / `Scope` fields; `Status` is one of:

`Draft` → `Reviewing` → `Accepted` → `Implemented`, or `Rejected` / `Superseded`

`docs/proposals/README.md` is the index: one row per proposal with current
status, updated as proposals move between states (rows are never deleted, so
the table stays a full history). See that file for the current list.

Completed architecture decisions and durable invariants that later work must
preserve live in `docs/decisions/` instead — those are settled, not proposed.

This is deliberately lighter than GitHub Issues/Projects. Revisit if the
number of contributors or concurrent proposals grows enough that a plain
Markdown index stops being enough to track state.

## Versioning

Single version number for the whole system (backend + frontend are deployed
together; there is no reason to version them separately).

**Git tags (`vMAJOR.MINOR.PATCH`, e.g. `v0.1.0`) are the only source of
version truth.** There is no `VERSION` file and no `version` field in
`frontend/package.json` — do not add either back. SemVer meaning:

- `MAJOR` — incompatible changes (API/protocol/schema break)
- `MINOR` — new functionality, backward compatible
- `PATCH` — bug fixes only

The running orchestrator's version is derived at build time from git, not
from a committed file:

```bash
cd orchestrator
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o bin/orchestrator ./cmd/orchestrator
```

This is embedded as `main.version` (defaults to `"dev"` when built without
the `-ldflags`, e.g. plain `go build`/`go run`), logged once at orchestrator
startup, and returned by `GET /healthz` as `{"status":"ok","version":"..."}`
— use that endpoint to confirm what a running deployment is actually on.

## CI

GitHub Actions (`.github/workflows/ci.yml`) runs on every push and PR to
`main`:

- **backend**: `go build ./...`, `go vet ./...`, `go test ./...`, inside the
  `golang:1.27.1` container image. It runs in a container (not the bare
  `ubuntu-latest` runner) because several `internal/store` /
  `internal/artifacts` tests assert that directories are root-owned or
  `chown` them to arbitrary uids — matching the fact that the orchestrator
  runs as root in production (see [operations.md](./operations.md)). The
  runner's default non-root user fails those tests for privilege reasons
  unrelated to correctness. The container also needs
  `git config --global --add safe.directory "$GITHUB_WORKSPACE"` before any
  `go` command, or `go build`'s VCS-stamping step fails with "dubious
  ownership" — `actions/checkout` leaves the tree owned by a uid that
  doesn't match what git expects when the job runs as the container's root
  user.
- **frontend**: `npm ci`, `lint`, `typecheck`, `test`, `build`, on the
  default `ubuntu-latest` runner (no root-ownership assumptions here).

CI is verification only — it does not deploy anywhere. See
[verification.md](./verification.md) for what each suite covers and for the
live-smoke steps CI cannot exercise (real gVisor host required).

## Cutting a release

The release workflow now builds a complete version, not just source archives.
It first runs the reusable CI against the exact tag, builds static amd64 backend
and deployment binaries plus a Next standalone package with its own Node runtime,
and publishes a generic sandbox artifact to GHCR by digest. A manifest connects
all components, their hashes, schema requirements and qualified runsc version.

A release remains unpublished until both Ubuntu 22.04 and 24.04 isolated native
qualification jobs succeed. The publisher verifies uploaded assets before
changing the GitHub Release draft to public. Existing public assets and sandbox
tags are never overwritten. Use a new `-rc.N` for a rebuilt candidate.

Configure the isolated builders, qualification hosts, environment permissions and
`HARNESS_RUNSC_VERSION` repository variable before pushing a candidate tag. These
resources have not been provisioned by the repository change; the workflow will
remain blocked without them. Never use a machine with production data as the
privileged builder/qualification runner.

Production selects an exact release and pulls it with `harness-deploy`, or imports
an offline bundle. No production Git, Go, npm or Docker is required. Configuration
and persistent data remain local to the instance. The prior manual source-build
instructions in operations.md describe legacy deployments and are not the new
managed upgrade path.

See [the delivery runbook](delivery/operations.md) for provisioning, adoption,
preflight, backup, migration, staged activation and rollback. See
[progress](delivery/progress.md) for what is implemented versus externally verified.
Restoring a binary alone is not a supported generic database rollback.

## What's deliberately not here yet

- **Central push deployment**: production remains host-initiated through the delivery tool.
  CI publishes artifacts; it does not receive production SSH access. Privileged
  release runners must be isolated from production.
- **`develop` branch / GitFlow**: not worth the merge overhead at the current
  scale (effectively single-contributor, short-lived feature branches).
  Reconsider if the number of concurrent contributors grows.
- **Issue tracker**: `docs/proposals/` covers this need lightly today;
  migrate to GitHub Issues/Projects if proposal volume or the number of
  people tracking them grows past what a Markdown index can hold.
