# Development

Use a clean source checkout, fresh Project database and disposable engine state.
Go uses `orchestrator/go.mod`; host Node uses `.node-version`. The guest Node/Pi
pins are independent runtime protocol inputs, documented in [image assembly](sandbox-image.md).
Dependencies are pinned in Go modules and the frontend lockfile. The application
imports public Khora SDK/Connect packages only; do not patch engine internals or
its database to emulate a product capability.

Keep code abstractions, packages, protocols, temporary paths and test harness
identifiers brand-neutral and named for their function. Product names belong in
documentation and presentation defaults. Frontend components and metadata read
the shared display-name setting from `frontend/lib/ui-config.ts`; translation
catalogs must not embed the product name.

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

Individual gates and optional measurement/browser/runtime suites are defined in
[verification](verification.md). Local and GitHub CI use the same check script.
The frontend gate builds once and runs deterministic UI tests against that build.

The frontend development server needs the Go edge for product HTTP/SSE/WebSocket.
Configure an explicit loopback-only development actor or the normal credential
registry. `PROJECT_DEV_ORIGINS` is a comma-separated development host allowlist.
A gateway-enabled application needs non-loopback frontend/model/engine endpoints
because its dedicated UID remains subject to Khora's network floor.

[dev/verify-khora.sh](../dev/verify-khora.sh) builds selected test executables and
runs them in disposable mount/network/PID/cgroup namespaces against a separate
btrfs image. It never addresses a live engine or database. The reusable
[substrate](../dev/project-substrate.sh) also accepts a custom test command.
See [dev/README.md](../dev/README.md) for prerequisites and output paths.

Release integration launches the actual host executable with strict configuration,
owner-only Unix APIs, mTLS engine endpoint, independent gateway and final guest
image. Model fixtures run outside the image. Lower-level fault tests may install
deterministic tools into disposable placements; those tools never enter a release
manifest. A candidate bundle supplies the exact deployable owner and frontend.

Use [delivery](delivery.md) to build, qualify and inspect a candidate. Keep images,
logs, credentials and screenshots outside public source. A build or simulated UI
pass does not substitute for the corresponding real runtime gate.
