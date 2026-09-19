# Verification

Use the current toolchain pins in
[scripts/release/toolchains.json](../scripts/release/toolchains.json). Go runtime
paths require Linux. Some tests assert root-owned directories, so CI runs the Go
suite as root in its disposable container.

## Source checks

```bash
go -C orchestrator build ./...
go -C orchestrator vet ./...
go -C orchestrator test ./...

cd frontend
npm ci
npm run lint
npm run typecheck
npm test
npm run build
cd ..

python3 -m unittest discover -s sandbox-image/tests -p 'test_*.py'
python3 -m unittest discover -s scripts/release -p 'test_*.py'
python3 scripts/check-public-docs.py
```

Do not run root test suites against production directories. Unit/integration tests
use temporary synthetic files, databases and fake services. Test data must use
synthetic identities and endpoints; actual schemas/data belong in private
operational evidence.

## What the tests establish

- Store/runtime/bridge/server tests exercise persistence, ownership, turn and
  generation transitions, migration shapes, proxy authorization, recovery and
  API behavior.
- Frontend tests cover reducers, runtime state, event replay and other pure UI
  behavior; lint/typecheck/build verify the app surface.
- Python bridge tests cover the in-sandbox queue/Pi protocol and image invariants.
- Release tests cover public artifact allowlists, report scope and publication.
- Documentation checks enforce English reference docs, live local links and no
  tracked private directory or retired proposal/announcement archive.

## Hosted artifact checks

A tag builds release binaries and rootfs, then uses Ubuntu 22.04/24.04 runners to
verify archive hashes, embedded version/commit, bundled Node, frontend standalone,
synthetic fresh/legacy database migration and actual runsc sandbox startup.
Reports explicitly mark production verification false. CI uses no production
credentials or real model/data gateway.

## Deployment-local acceptance

Before opening traffic, the target operator validates the actual backup and
restore point, configured services and gateways, retained conversation/files/KB,
new model turns, Stop/queue behavior, sandbox cold start and scheduled work.
Internal smoke runs with ingress blocked and automatic jobs held.

A code-test pass does not prove a particular historical database is compatible.
Use a consistent private backup for preflight/rehearsal. Unknown schema, failed
integrity checks or an incomplete external writer inventory block the upgrade.
Do not upload real backup contents, host inventory or raw failure logs to public
CI. Keep only sanitized technical assertions in release artifacts.

## Workbench performance checks

Browser tests use deterministic synthetic messages/files, never a live model or
production database. Install the pinned browser with `npx playwright install
chromium` from `frontend/`; Linux also needs the corresponding Playwright system
libraries. `PLAYWRIGHT_CHROMIUM_EXECUTABLE` can select the matching installed
Chromium executable when the automatic download is unavailable.

```bash
cd frontend
npm run test:e2e
HARNESS_PERF_PRODUCTION=1 npm run test:e2e
npm run test:performance
cd ..
go -C orchestrator test ./internal/store -run '^$' \
  -bench BenchmarkWorkbenchPages -benchtime=5x -count=1
```

The production browser suite builds and launches the standalone app. Its
`.next-perf` output and port 3187 are isolated from the regular development
server. `/perf-fixtures/virtual` returns 404 unless `HARNESS_PERF_FIXTURES=1`.
The browser suite checks dynamic heights, prepend anchors, restoration, bounded
DOM counts, shared reads and full-body loading. Performance cases collect 30
switches at each history scale, simulated network latency/throughput, 100-switch
heap growth after GC, and a CPU-throttled scenario. Results and attachments live
in ignored `frontend/test-results/`; record the browser, CPU, build and whether
HTTP was mocked when comparing runs.

A bounded in-memory diagnostic ring is available as
`window.__workbenchPerformance()`: stages contain timestamps and durations, not
message bodies, file paths or bearer URLs. `select` to `readable` measures
application work separately from test-runner click/poll overhead. Resource-stage
marks separate response headers, response body completion, JSON parsing and
message merge. Browser measurements use production assets, but synthetic HTTP
fixtures do not establish real deployment latency, compression or backend load.
Verify these separately against an authorized deployment and retain sanitized
measurements. Do not claim that a microbenchmark or mocked browser run proves
production acceptance.

## Bridge projection recovery

The bridge journal tests use synthetic output larger than 64 KiB and exercise
SSE pruning, contradictory duplicates, missing sequence refusal, reconnect
watermarks, transaction rollback and stale checkpoint writes. Server projection
tests simulate restart with partial text and pending tool state, loss before and
after projection commit, completion retries and durable message-event replay.
They do not establish Khora process transport or substrate acceptance.

```bash
sudo go -C orchestrator test -race ./internal/store ./internal/server \
  -run 'TestBridge(Journal|Projection|Completion)'
python3 scripts/release/generate-schema-baselines.py
```

The schema generator only opens temporary synthetic databases. It preserves
historical DDL fixtures and reviewed fingerprints while deriving current targets
from the checked-out store implementations. Review catalog changes with schema
changes; deployments still use the explicit offline migration command.

## Khora application template acceptance

On an isolated engine built at `14174318159f`, with manual cold recovery enabled,
a positive memory admission budget, dedicated quota-capable btrfs storage and a
built application template, run:

```bash
KHORA_INTEGRATION_ENDPOINT=http://127.0.0.1:18191 \
KHORA_INTEGRATION_TEMPLATE=battery \
go -C orchestrator test -tags khoraintegration ./internal/sandbox/khora \
  -run '^TestKhoraTemplateShellAndNativeHistory$' -count=1 -v
```

This test creates synthetic volumes and a sandbox, runs the shell through the
actual process stdio journal, checks a compute-free workspace read while paused,
and verifies Pi 0.77.0 settings and native session selection after cold resume.
It uses the real Pi SessionManager to seed history without a model request.
It does not establish model/Doris/OBS gateway integration or a fresh reproducible
release build. Use a disposable engine: the fixture explicitly destroys only
its own sandbox and volumes during cleanup.
