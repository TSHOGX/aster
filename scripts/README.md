# Repository and delivery tools

- `check.sh`: shared source, backend, frontend and race gates for local/CI use.
- `check-lockfile-registry.sh`: portable frontend dependency URLs.
- `check-public-docs.py`: English references, local links and private boundaries.
- `fetch-go-dependencies.sh`: explicit pinned private-module access for CI.
- `candidate.py build|verify|inspect`: source-only host/standalone frontend
  bundles, artifact integrity and read-only deployment identity inspection.
- `tests/`: candidate corruption, missing/extra file, mode and symlink rejection.

See [verification](../docs/verification.md) for gate coverage and
[delivery](../docs/delivery.md) for candidate and activation procedures. None of
these commands deploys services or resets installation data.
