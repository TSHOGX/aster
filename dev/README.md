# Development helpers

`verify-khora.sh` runs real application tests inside disposable mount, network,
PID and cgroup namespaces with a private `/run` and btrfs backing storage. It builds
or accepts the pinned engine. No private script, sibling checkout or fixed gateway
account is required. Application code still uses only public engine APIs.

## Prerequisites

The host needs Go, Python, runsc, iproute2, btrfs tools, Linux namespaces and writable
cgroup v2 delegation. Gateway identity requires `SO_PEERPIDFD` (Linux 6.5+).
Private module access or a populated Go cache is needed for the pinned Khora build.
Build a fresh [application template](../docs/sandbox-image.md) first.

From the repository root (replace the template and output paths):

```bash
sudo env "PATH=$PATH" "GOPATH=$(go env GOPATH)" "GOCACHE=$(go env GOCACHE)" \
  bash dev/verify-khora.sh \
  --template-dir /absolute/path/templates/battery \
  --manifest /absolute/path/templates/battery.manifest.json \
  --output /absolute/path/private/verification-output
```

`--engine FILE` accepts an existing pinned engine. Without it the script builds
from `orchestrator/go.mod` with the required engine version ldflag. `--via-user`
selects an existing non-root account (default `nobody`). The script exports that
account's UID/GID to tests; the root engine and gateway must have distinct UIDs.

The template is mounted read-only. The script creates an 8 GiB sparse btrfs image
and disposable engine volumes, cgroups, sockets and network interfaces; cleanup
removes the substrate after tests. Its controlled upstreams are inside the isolated
network. Raw `tests.log`, `engine.log` and `revisions.json` are copied to `--output`
on success or failure. Keep that directory private and separate from user storage.

## Selection and browser checks

`--run REGEX` defaults to `^TestKhora`. `--timeout DURATION` defaults to a five-minute
per-package test deadline. Packages run serially against one engine; workload tests
create explicit concurrency internally. See [verification](../docs/verification.md)
for the full source and integration matrix.

`--browser` enables the daemon test's real Chromium journey. Install frontend
packages and Chromium as the ordinary user first, then pass the executable path
through sudo so the root test process can use that installation:

```bash
(cd frontend && npm ci && npx --no-install playwright install chromium)
khora_browser=$(cd frontend && node --input-type=module -e 'import { chromium } from "playwright"; console.log(chromium.executablePath())')
sudo env "PATH=$PATH" "GOPATH=$(go env GOPATH)" "GOCACHE=$(go env GOCACHE)" \
  "PLAYWRIGHT_CHROMIUM_EXECUTABLE=$khora_browser" \
  bash dev/verify-khora.sh \
  --template-dir /absolute/path/templates/battery \
  --manifest /absolute/path/templates/battery.manifest.json \
  --run '^TestKhoraDaemonStartsWithoutEmbeddedRuntime$' --browser \
  --output /absolute/path/private/browser-output
```

Chromium also needs its host shared libraries. The browser script starts Next on
an ephemeral port in the test namespace and uses the real daemon through its proxy.
It shares `frontend/.next` with ordinary Next runs, so stop other frontend dev/build
processes before running it. The six fixture browser checks are a separate suite.

## Sustained workload

Select `--run '^TestKhoraSustainedRetainedWorkload$'` to run at least 60 seconds with
two shell sessions, 32 KiB output per normal turn, repeated pause/resume and owner
replacement during two eight-second turns. It checks exact workspace counters and
message identities, then prints `SUSTAINED_RESULT` with latency, logical storage
accounting and released execution capacity. Capture and model gateway interruptions
are tested separately. See [measurements](../docs/development-evidence.md).

`build-dns-pin.sh` is an optional DNS helper for rootfs build hosts. These development
scripts do not perform production migration or deployment.
