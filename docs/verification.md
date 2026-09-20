# Development verification

Run from the repository root:

```bash
go -C orchestrator build ./...
go -C orchestrator vet ./...
go -C orchestrator test ./...
go -C orchestrator test -race ./internal/store ./internal/execution ./internal/bridge ./internal/server ./internal/sessionfiles ./internal/gateway ./internal/trajectory
python3 -m unittest discover -s sandbox-image/tests -p 'test_*.py'
python3 scripts/check-public-docs.py
bash -n sandbox-image/build-rootfs.sh sandbox-image/build-khora-template.sh
```

In `frontend/`, run `npm run lint`, `npm run typecheck`, `npm test` and
`npm run build`. Playwright checks the full workbench against a running development
backend; it is separate from reducer/component unit tests.

Real Khora integration requires the pinned engine and a freshly finalized template.
The [portable isolated harness](../dev/README.md) provisions disposable prerequisites
and records exact inputs. Its sustained workload reports latency, capacity and
logical storage growth with explicit fault injections.
Set `KHORA_INTEGRATION_ENDPOINT`, `KHORA_INTEGRATION_TEMPLATE` and
`KHORA_INTEGRATION_MANIFEST`, `KHORA_INTEGRATION_VIA_UID` and
`KHORA_INTEGRATION_VIA_GID` to isolated test inputs, then run:

```bash
go -C orchestrator test -p 1 -tags khoraintegration ./internal/sandbox/khora ./internal/server ./internal/retainedapp ./cmd/orchestrator -run '^TestKhora' -v -count=1 -timeout=180s
```

The packages share one disposable engine budget and run serially (`-p 1`).
Concurrency is exercised explicitly inside the workload tests, rather than by
unrelated application fixtures competing for engine capacity.

Without those inputs the tagged tests may skip. A skip is not real-engine evidence.
Record the exact test names, engine/template pins and results. Exercise shell and Pi
turns, native history restore, interrupted journal recovery, workspace mutations,
owner replacement and daemon restart/retirement. Test failures must be resolved;
deferring production acceptance does not defer development correctness.

Production migration, rollout, rollback and deployment acceptance are a separate future
project. These development tests neither perform nor claim that work.
