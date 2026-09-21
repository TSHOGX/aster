# Isolated runtime verification

Prerequisites: Linux with mount/network/PID namespaces, cgroup v2 delegation,
btrfs with simple quotas, runsc, iproute2/nftables, Go, Python, sudo and sufficient
disposable disk/memory. The harness creates a sparse 16 GiB btrfs image, an isolated
network and private engine state. It never attaches an existing engine database.

Build an immutable [Project image](../docs/sandbox-image.md), then run as the
normal build user (the script invokes `sudo -n` for the substrate):

```sh
dev/verify-khora.sh --template /srv/build/battery --manifest /srv/build/battery.manifest.json --output /srv/qualification/run --suite release
```

Suites are `release`, `mixed`, `browser`, `faults` and `all`. The default is the
actual executable/image release gate. `mixed` runs over five minutes. `browser`
requires `npm run build` in the frontend and an installed Playwright Chromium.
Provide `PLAYWRIGHT_BROWSERS_PATH` if using a nondefault cache. The script builds
the pinned engine or accepts `--engine FILE`; module caches remain with the build
user. No private checkout path is compiled into the public test harness.

Logs, revision metadata, screenshots and workload measurements go to the explicit
output directory. The disposable filesystem is removed after namespace teardown;
the named output directory is retained. Use an untracked location and review its
contents before sharing. Never place secrets in test fixture arguments.

`project-substrate.sh COMMAND...` can run an already built test command with
`PROJECT_TEST_ENGINE`, `PROJECT_TEST_TEMPLATE` and `PROJECT_TEST_OUTPUT` set.
Optional `PROJECT_TEST_VIA_UID/GID` select its non-engine gateway identity; defaults
use UID/GID 65534. The executable gate configures an owner-only private API socket,
a disposable mTLS engine proxy and reachable non-loopback model/frontend origins.

See [verification](../docs/verification.md) for acceptance criteria and coverage.
