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
KHORA_INTEGRATION_MANIFEST=/srv/batterygpt/releases/battery.manifest.json \
go -C orchestrator test -tags khoraintegration ./internal/sandbox/khora ./internal/server ./internal/retainedapp \
  -run '^TestKhora(ApplicationCompositionServesHTTPWithoutEmbeddedRuntime|TemplateShellAndNativeHistory|ExecutionWorkerCommitsTurnMessagesAndJournal|PiExecutionWorkerCommitsNativeState|InterruptedRecoveryFlushesMissingCompletion)$' -count=1 -v
```

This test creates synthetic volumes and a sandbox, runs the shell through the
actual process stdio journal, checks a compute-free workspace read while paused,
and verifies Pi 0.77.0 settings and native session selection after cold resume.
It uses the real Pi SessionManager to seed history without a model request.
It does not establish model/Doris/OBS gateway integration or a fresh reproducible
release build. Use a disposable engine: the fixture explicitly destroys only
its own sandbox and volumes during cleanup.

The Pi worker case uses a local synthetic Anthropic-compatible event stream,
without upstream credentials. It verifies product turn/message/journal commits
and native-state advancement through the confined helper. Both worker cases use
the HTTP message handler and execution controller to pause after the first turn and run a second turn in
the same retained sandbox at the next epoch. The Pi case checks that its native
session ID and path survive the transition. This is driver and
control-plane acceptance, not production model gateway or entitlement acceptance.
The manifest input is the application-owned export for the tested template. The
worker tests derive real specification digests from validated plans and require
the guest's advertised manifest digest, including on a readiness retry.

The application-composition case creates a server with no embedded runtime or
host artifact watcher. It creates a shell session through HTTP, uploads before
the first turn, runs that turn and downloads its output through the composed
retained services. A legacy repair/lease pass during the active turn must leave
the Khora generation unchanged. Inventory and reconciliation workers then run
under the composed runtime's lifetime.
The shell case also injects a failure after protocol readiness but before binding
acceptance, then retries the same process key and recovers readiness after its
original hello has been acknowledged.
Both worker cases replace the product owner after the second turn and let the
background reconciler adopt the idle process at the same epoch. An injected
acceptance failure requires another attachment; assertions require zero new
process starts and unchanged generation/handle/epoch. A third turn then completes
through that connection. Pi keeps the same native session throughout.

The shell case then blocks host ingestion after a fourth turn starts, waits for
the guest to journal a 256 KiB output and completion, and interrupts the carrier.
The interrupted-recovery service pauses, resumes for historical replay, projects
the old output exactly once and pauses again before classifying the turn as
failed. It retries injected lost ingest and stopped-reader replies and verifies
that recovery retains the full producer payload beyond the engine's process
tail. A separate case interrupts a still-running producer after output, before
completion, to exercise the host failure boundary and partial-output projection.

The Pi case also interrupts a fourth turn before host completion ingestion,
recovers its output, restores the exact completed native digest, and preserves
the interrupted native file. It retries a lost restore reply, resumes the same
sandbox, and completes a successor turn while keeping the interrupted turn failed.
It then captures all four completed snapshots from earlier producing epochs,
checks every archive against its recorded digest, retries an injected lost
receipt, and verifies exactly four raw-source records. A paused source requests
maintenance activation. The controller resumes with zero active execution
bindings, captures the backlog, pauses again and releases its active-capacity slot.
The shell case reads workspace files through the HTTP range-download path while
live and while paused. It rejects symlink parents and missing files, verifies
that a held offline read blocks resume, and checks that closing/error paths release
the lease without activating compute.
The live shell case also writes through the HTTP editor handler, injects a lost
helper completion reply, and verifies recovery without a second replacement.
A second fault case changes the destination independently before recovery and
checks that the changed content survives and the original mutation becomes failed.
The paused-upload case injects lost resume, replacement and pause replies in one
workflow, verifies exactly one replacement process, reads the result without
compute, and then resumes a normal agent in the retained sandbox.
Before any shell execution, the test uploads through HTTP to a new session,
asserts that no execution binding was allocated and the environment is paused,
then verifies the first turn reads those uploaded bytes.

Process-control acceptance interrupts a long-running shell command through the
HTTP Stop handler, verifies queued prompts remain parked, and retries the same
abort ID without repeating delivery. The Pi case sends two queued prompts as
steering, retries their receipts, aborts a blocked model request, and runs a later
turn before historical recovery. The gateway flow tests separately verify that
revoking one turn's grant drains its work without disabling a later grant.

Background reconciliation tests inject a partial abort input and a lost upload
completion reply, then let the worker settle them without another HTTP request.
The Pi capture test uses the same worker for maintenance resume and final pause.
Concurrency tests hold an upload request open while recovery waits and verify
that the helper is not signaled. Separate-loop tests retain a blocked action past
its deadline while other work progresses, and verify shutdown waits for that
action rather than starting a replacement.

Steering acceptance injects a partial process-input send, leaves the source row
parked, and waits for background retry and the guest's journaled delivery result.
It checks one transcript message per source prompt. Store tests cover late
delivery after the target turn ended, rejected delivery, atomic event rollback,
historical outcome replay and explicit unknown classification after interruption.
Artifact checks scan a 40-file directory through multiple guest pages while live
and through volume listings while paused. They verify uploaded files appear,
stale index rows disappear, symlink descendants remain absent and paused scans
keep the active-capacity count at zero. Unit tests cover incomplete scans,
overflow, stale scans, lifecycle/upload races and atomic event/index rollback.
