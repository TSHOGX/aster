# Development verification

Run source checks from the repository root:

```bash
go -C orchestrator build ./...
go -C orchestrator vet ./...
go -C orchestrator test ./...
go -C orchestrator test -race ./internal/store ./internal/execution ./internal/bridge ./internal/server ./internal/sessionfiles ./internal/gateway ./internal/trajectory ./internal/nativecapture
python3 -m unittest discover -s sandbox-image/tests -p 'test_*.py'
python3 scripts/check-public-docs.py
bash -n dev/verify-khora.sh dev/khora/run.sh dev/khora/substrate.sh sandbox-image/build-rootfs.sh sandbox-image/build-khora-template.sh
git diff --check
```

In `frontend/`, install the locked dependencies and run:

```bash
npm ci
npm run lint
npm run typecheck
npm test
npm run build
npx --no-install playwright install chromium
npm run test:e2e -- --grep-invert 'performance|CPU slowdown'
```

The six browser correctness fixtures use controlled API/SSE responses and a local
Next server; they do not validate a live daemon. Optional performance scenarios
run separately with `npm run test:performance`. Performance results depend on the
host and must include the measurement conditions.

## Real Khora integration

Use the [portable harness](../dev/README.md) with a finalized template and exported
manifest. It creates disposable namespaces, engine storage and synthetic upstreams,
and records source revision, dirty status and engine/template hashes. Pass `--browser`
to include Chromium through the Next proxy to the real daemon; that check also needs
frontend dependencies and an explicit browser executable when running under sudo.

The harness selects `^TestKhora` and a five-minute **per-package** timeout by default.
Packages run serially because they share one engine budget. Concurrency is explicit
inside the two-session sustained workload. The composed gateway test waits for an
actual grant expiry, and the sustained test runs for at least 60 seconds; allow
several minutes for the complete suite.

If providing your own equivalent isolated substrate, set all five variables:
`KHORA_INTEGRATION_ENDPOINT`, `KHORA_INTEGRATION_TEMPLATE`,
`KHORA_INTEGRATION_MANIFEST`, `KHORA_INTEGRATION_VIA_UID` and
`KHORA_INTEGRATION_VIA_GID`. Then run:

```bash
go -C orchestrator test -p 1 -tags khoraintegration \
  ./internal/sandbox/khora ./internal/server ./internal/retainedapp ./cmd/orchestrator \
  -run '^TestKhora' -v -count=1 -timeout=5m
```

Missing inputs cause tagged tests to fail or skip, depending on the fixture. Neither
outcome counts as real-engine evidence. Inspect test names and final exit status;
a successful package with “no tests to run” proves nothing about that package's
integration paths.

## Recording results

Keep raw logs and machine-specific metadata private. Publish only sanitized synthetic
measurements with the source revision and dirty status actually recorded at execution.
A later documentation commit does not become a tested revision. Distinguish HTTP
handler composition from a launched daemon and fixture browsers from real browsers.
See [development evidence](development-evidence.md) for the recorded runs and limits.

Tests cover policy/store invariants, owner/epoch fencing, lost replies, Pi and shell
turns, control, native restore, capture restart, workspace mutations, sharing/auth,
SSE replay, gateway expiry/teardown and retained retirement. Power-loss durability
and production migration/deployment acceptance are separate from these development checks.
