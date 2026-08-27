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
python3 -m unittest sandbox-image/tests/test_agents_knowledge.py
python3 -m unittest dev/mcp-gateway/test_gateway.py
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

For web/security changes, also smoke the publish HTTPS path through nginx:

```bash
curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:9000/api/healthz
curl -sk -D - -o /dev/null https://127.0.0.1:9000/ \
  | grep -iE 'content-security-policy|strict-transport|x-frame-options|x-content-type|referrer-policy|permissions-policy|cross-origin-opener'

# X-Forwarded-For spoofing must not bypass login lockout: rotating fake XFF
# should still hit 429 after the 5th bad attempt from the same real client IP.
for i in $(seq 1 7); do
  curl -sk -o /dev/null -w "attempt $i -> %{http_code}\n" \
    https://127.0.0.1:9000/api/login \
    -H 'Content-Type: application/json' \
    -H "X-Forwarded-For: 10.0.0.$i" \
    -d '{"username":"nonexistent_probe_user","password":"wrong"}'
done
```

## Platform note

The Go control plane uses Linux-only syscalls (e.g. `SO_PEERCRED` for the
proxy-correlation socket) and shells out to `runsc`, `ip`, and `nft`. It builds and
tests fully only on Linux; a real gVisor host is required for live smokes and the
sandbox-launch paths.

There is no formal release-qualification gate today; CI (build/vet/test) plus the
live smoke above are the bar. If a release-gate process is needed later, build it
against the then-current contract.
