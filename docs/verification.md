# Verification

Install Go from `orchestrator/go.mod`, Node from `.node-version`, Python 3 and the
locked frontend dependencies. Node is also required by the Go native RPC tests. Their isolated npm package and
lockfile live in `orchestrator/internal/runtimerelease`; no local guest rootfs
or privileged image build is needed. Installation scripts are disabled because
these tests use controlled model streams, not the optional native tools.

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r frontend/e2e/requirements.txt
npm --prefix frontend ci
npm ci --ignore-scripts --prefix orchestrator/internal/runtimerelease
cd frontend
npx --no-install playwright install chromium
cd ..
scripts/check.sh all
scripts/check.sh race
```

On a new Linux browser runner use Playwright's `--with-deps` option. The check
script never installs dependencies, changes credentials or contacts a live
installation. `all` means the unprivileged repository gates below; runtime
qualification is an explicit separate command.

## Test layers

| Gate | Entry point | Coverage and limits |
| --- | --- | --- |
| Source | `scripts/check.sh source` | Documentation links/public boundary, lockfile registry, shell syntax and candidate integrity tests |
| Backend | `scripts/check.sh backend` | Build, vet, all ordinary Go tests, Node RPC handler/full Session tests, compile all `khoraintegration` packages and execute their substrate-independent measurement tests |
| Core races | `scripts/check.sh race` | Product, Agent runner and runtime release under the Go race detector |
| Frontend | `scripts/check.sh frontend` | Lint, types, Vitest, one production build and deterministic Chromium UI tests against that build |
| Cross-browser controls | `npm --prefix frontend run test:pickers` | Scheduling controls in Chromium, Firefox and WebKit; install those browsers first |
| Runtime | `dev/verify-khora.sh ... --suite all` | Actual owner, immutable guest image, native Pi, Khora, network/files, browser, faults, realtime and sustained mixed workload |
| Extended workload | `dev/verify-khora.sh ... --suite combined` | Explicit two-tab workload, network profiles and active-owner update |
| Provider | `dev/verify-khora.sh ... --suite provider` | Explicit external-provider continuity; requires private configuration and incurs provider usage |

The UI suite uses controlled API fixtures; it does not qualify the Go edge,
SSE transport or native runtime. `test:e2e` is the browser component invoked by
the isolated runtime harness, not a standalone test against an arbitrary URL.
`test:ui:built` serves standalone output with its static/public assets from a build
produced in the same checkout, matching the candidate layout; `test:ui` and
`test:pickers` build first for direct developer use.

UI setup regenerates bounded synthetic Office documents from
`frontend/e2e/requirements.txt`; accessibility and replacement tests run by
default. `PYTHON` can select an existing fixture virtualenv interpreter.
The synthetic corpus includes 41 and 200 slide presentations, 15,599 Word
paragraphs with a 3,000-by-5 table, and 22,962-by-12 spreadsheets. UI setup builds
the actual preview worker and derives SQLite fixtures from these documents.
`office-reading.spec.ts` checks end reachability, find, bounded DOM, mobile fit,
and lease cleanup. `PREVIEW_TEST_WORKER=/absolute/path/to/preview-worker go test
./internal/product -run TestPreviewJobs` exercises the real Linux process sandbox,
deduplication, range windows and cleanup; it requires `bwrap` and `prlimit`.
Run
`npx playwright test --config playwright.ui.config.ts e2e/ui/preview-slides.spec.ts`
from `frontend/` after building to verify direct navigation through each deck.

The instrumentation benchmark requires `INTERACTION_BENCHMARK_OUTPUT`; startup
comparison tests require `STARTUP_PROFILE_OUTPUT`. Other optional profiles declare
their `*_PROFILE_OUTPUT`, `*_LATENCY_OUTPUT` or `HISTORY_WINDOW_OUTPUT` in the
individual `frontend/e2e/ui/*-budget.spec.ts` and preview/profile tests. Those scenarios
remain opt-in and their skips do not count as measured performance. The ordinary
Go suite retains pressure/scale tests without `-short`. A failed test is not
made green by automatic retries; preserve intermittent failures and investigate
before treating a repeated pass as stability evidence.

## GitHub CI

[CI](../.github/workflows/ci.yml) runs on pull requests and `main` pushes, with
manual/reusable entry points. Feature-branch pushes do not duplicate PR runs;
archive branches do not trigger obsolete workflows. Concurrency cancels stale
runs for the same workflow/ref. All jobs have bounded timeouts and read-only
repository permissions. Main/manual/release checks add the core race gate.
Release candidates also run Firefox/WebKit scheduling controls. Manual CI can
select `cross_browser`; daily Chromium checks reuse the same production build.

Configure repository or organization Actions secret `KHORA_READ_TOKEN` with
read-only access to the pinned private `TSHOGX/khora` repository. The default
`GITHUB_TOKEN` cannot read a different private repository. Dependency access is
required even on a warm module cache; absence fails explicitly instead of
silently skipping the backend. Never use `pull_request_target` to expose this
secret to untrusted PR code. Fork PRs can run source/frontend checks, but trusted
backend qualification must run in a reviewed context with the secret available.

Failed deterministic UI/picker runs retain their `frontend/test-results` subdirectories for seven days.
Do not upload raw production or privileged/provider reports through this job.
Candidate artifacts have their own bounded retention and are not published as
GitHub Releases; see [delivery](delivery.md).

## Runtime qualification

Build a new immutable template with `sandbox-image/build-khora-template.sh`.
Use [the isolated harness](../dev/README.md) with the exact exported manifest and
pinned engine. A candidate bundle can be supplied with `--candidate DIR`; the
harness verifies its inventory and source revision, then uses its owner executable
and standalone frontend. Each invocation requires a new output directory so old
logs cannot masquerade as new evidence. Fingerprints include source files,
executables, engine, manifest, frontend build and candidate manifest hashes.

`all` includes release, files, network, mixed, browser, realtime and faults.
`gateway` and `native` are focused subsets of `release`; `provider` and `combined`
are deliberately excluded from `all`. See [dev/README.md](../dev/README.md) for
prerequisites and individual suite meanings. Compilation in hosted CI does not
claim these privileged gates passed.

## Evidence invariants

- A successful receipt is durable admission, not proof of consumption.
- One prompt has one effective consumption/handled claim; unknown is not reusable.
- Stop preserves its original Run target and draft through concurrent actions.
- Failed core queue cleanup cannot emit successful reconciliation; process recovery
  retains original journal evidence. Observer failure cannot strand proven cleanup.
- Native user events cannot relocate consumed input anchors; there is no legacy
  fallback. Late tool updates retain their original segment/Run ownership.
- Page, delta, reconnect, rebuild, share and export chronology agree.
- Exact schema/protocol, executable and runtime identities remain mandatory.

After a separately authorized live activation, record the actual installed
identities and smoke results. Historical deployment evidence only describes its
recorded commit. Reset-specific acceptance belongs in [bootstrap](bootstrap.md);
ordinary current-format updates use [operations](operations.md).
