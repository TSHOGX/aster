# Verification & Regression Checks

> How to verify a change before shipping. The tests live in the codebase itself;
> run them on a host with the Go toolchain, Node, Python, and (for live smokes)
> `runsc`.
>
> GitHub Actions runs `go build`/`go vet`/`go test` and the frontend
> lint/typecheck/test/build on every push and PR to `main`
> (`.github/workflows/ci.yml`) — see [release-process.md](./release-process.md).
> The backend job runs in a container as root because several store/artifacts
> tests assert root-owned paths, matching how the orchestrator runs in
> production. CI covers the same commands below; live smokes and sandbox-image
> bridge tests still require a real gVisor host and are run manually.

## Standard checks

```bash
# Go control plane
(cd orchestrator && go test ./...)

# Frontend workbench
(cd frontend && npm run lint && npm run typecheck && npm test && npm run build)

# Sandbox image bridge/driver/entrypoint behavior
python3 -m unittest sandbox-image/tests/test_harness_bridge_client.py
```

Test fixtures live next to their tests:
`orchestrator/internal/runtime/testdata/`,
`orchestrator/internal/server/testdata/pi/`, and
`sandbox-image/tests/testdata/`.

## What each suite covers

- **`go test ./...`** — the bulk of correctness: store/schema, control-plane turn
  and generation lifecycle, bridge processor, runtime rendering + mount plan, OCI
  spec, canonical digests, auth, event stream parsing, and deployment resolution
  (driver ↔ provider ↔ image-manifest validation).
- **frontend** — the pure session-event reducer (`session-events.test.ts`) plus
  lint/typecheck/build. The reducer test is the key one: it proves live SSE and
  replay reduce identically and that non-terminal errors don't mark a session
  failed.
- **bridge-client unittests** — the in-sandbox protocol: file-queue ordering,
  hello/claim/ack/emit, the Pi runner command line and env hygiene, Pi
  config-materialization rejection, and entrypoint invariants (required uid/gid,
  secret-mount rejection).

## Live smoke

After changes to the runtime, bridge, proxy, deployment config, rootfs, or session
lifecycle, run an end-to-end smoke on a real gVisor host:

1. Build the rootfs for the deployed drivers:
   `SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh`
   (see [sandbox-image.md](./sandbox-image.md)).
2. Start the orchestrator, model proxy, and frontend
   (see [operations.md](./operations.md)).
3. Create an `agent` session (resolves to Pi), send a message, and confirm the turn
   completes: `ack_turn_started` → streamed output → `ack_turn_completed`, and the
   model proxy logs `started` / `200 OK` / `finished` for the turn.

## Platform note

The Go control plane uses Linux-only syscalls (e.g. `SO_PEERCRED` for the
proxy-correlation socket) and shells out to `runsc`, `ip`, and `nft`. It builds and
tests fully only on Linux; a real gVisor host is required for live smokes and the
sandbox-launch paths.

## History

An earlier `tools/release/` framework formalized `sandbox-isolation-v1` release
qualification (suites, gates, supplied-evidence labels, and static doc-pinning
checks). It was removed along with the phase-era docs it pinned, since it had no CI
wiring, no production dependency, and would have constrained forthcoming
architecture changes. If a formal release-gate process is needed again, rebuild it
against the then-current contract rather than reviving the old phase-pinned one.
