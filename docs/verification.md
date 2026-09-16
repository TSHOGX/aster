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

python3 -m unittest sandbox-image/tests/test_harness_bridge_client.py
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
