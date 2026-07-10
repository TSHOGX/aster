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
  `golang:1.22` container image. It runs in a container (not the bare
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

1. Make sure `main` is green (CI passed on the latest push).
2. Tag it:
   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```
3. Pushing a `v*` tag triggers `.github/workflows/release.yml`, which runs
   [git-cliff](https://github.com/orhun/git-cliff) over Conventional Commits
   since the previous tag and publishes the result as a GitHub Release —
   there is no committed `CHANGELOG.md` file to keep in sync; the tag/Release
   pair is the changelog. Every commit message must follow
   [Conventional Commits](https://www.conventionalcommits.org/) (`feat(x):
   ...`, `fix(x): ...`, `docs: ...`, etc.) for this to produce a useful
   changelog — this was already the repo's practice before CI existed.
4. Deploy manually (no CD yet — see below). Build first, then cut over systemd services:
   ```bash
   cd /home/agent-runtime-platform/main
   git fetch --tags && git checkout vX.Y.Z
   VERSION=$(git describe --tags --always --dirty)

   # Build backend to a staging binary so a failed build never corrupts the live binary.
   cd orchestrator
   go build -ldflags "-X main.version=${VERSION}" -o bin/orchestrator.new ./cmd/orchestrator

   # Build frontend production assets. If this is not run as watt, chown .next before restart.
   cd ../frontend
   npm run build
   # sudo chown -R watt:watt .next   # only if built as root

   # Atomic-ish backend binary swap + service restarts.
   cd ../orchestrator/bin
   sudo cp -a orchestrator orchestrator.bak.$(date +%s)
   sudo mv orchestrator.new orchestrator
   sudo systemctl restart harness-publish-orchestrator
   sudo systemctl restart harness-publish-frontend

   curl -s http://127.0.0.1:9090/healthz   # confirm the version field matches ${VERSION}
   curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:9000/api/healthz
   ```
   See [operations.md](./operations.md) for the full runbook (ports, systemd
   units, nginx/HSTS, security-header smoke checks, troubleshooting).

Rolling back is the same flow with an older tag: `git checkout v<previous>`,
rebuild, restart.

## What's deliberately not here yet

- **CD (automated deploy)**: deployment stays manual (step 4 above).
  Automating it means giving CI credentials to the production host, which is
  a real security tradeoff — worth reconsidering once deploys are frequent
  enough that the manual step is the bottleneck, e.g. via a self-hosted
  runner on the deploy host rather than handing out SSH keys.
- **`develop` branch / GitFlow**: not worth the merge overhead at the current
  scale (effectively single-contributor, short-lived feature branches).
  Reconsider if the number of concurrent contributors grows.
- **Issue tracker**: `docs/proposals/` covers this need lightly today;
  migrate to GitHub Issues/Projects if proposal volume or the number of
  people tracking them grows past what a Markdown index can hold.
