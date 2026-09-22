# Development

Use a clean source checkout, fresh Project database and disposable engine state.
Dependencies are pinned in Go modules and the frontend lockfile. The application
imports public Khora SDK/Connect packages only; do not reach into engine internals
or patch its database to emulate a product capability.

```sh
go -C orchestrator test -race ./...
go -C orchestrator vet ./...
python3 scripts/check-public-docs.py
cd frontend
npm ci
npm run typecheck
npm run lint
npm test
npm run build
```

The frontend development server needs the Go edge for product HTTP/SSE/WebSocket.
Configure an explicit loopback-only development actor or the normal credential
registry. `PROJECT_DEV_ORIGINS` is a comma-separated development host allowlist.
A gateway-enabled application needs non-loopback frontend/model/engine endpoints
because its dedicated UID remains subject to Khora's network floor.

[dev/verify-khora.sh](../dev/verify-khora.sh) builds the pinned test executables and
runs them in disposable mount/network/PID/cgroup namespaces against a separate
btrfs image. It never addresses a live engine or database. The reusable
[substrate](../dev/project-substrate.sh) also accepts a custom test command.
See [dev/README.md](../dev/README.md) for prerequisites and output paths.

The release integration package launches the actual host executable with its
strict JSON configuration, owner-only Unix APIs, mTLS engine endpoint, published
content, independent unprivileged gateway process and final guest image. Model fixtures run outside
the image. Lower-level fault tests may install deterministic fixture tools into
disposable placements; those tools are never part of the release manifest.

Keep generated images, logs, credentials and screenshots outside public source.
The [verification guide](verification.md) describes required evidence. A fake test
or successful build does not substitute for its corresponding real runtime gate.
