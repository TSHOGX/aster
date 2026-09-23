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

Suites are `release`, `gateway`, `native`, `provider`, `rollback`, `mixed`, `browser`, `realtime`, `faults` and `all`. The default is the
actual executable/image release gate. `gateway` isolates native streaming across
a temporary owner outage, gated gateway replacement and explicit owner handoff.
The harness builds a second executable with distinct version metadata to exercise
target identity. Its deterministic model does not qualify an external provider. `native` isolates real Pi retry, cancellation and post-run/preflight compaction
settlement using deterministic provider responses. `mixed` runs over five minutes. `browser`
requires `npm run build` in the frontend and an installed Playwright Chromium.
Provide `PLAYWRIGHT_BROWSERS_PATH` when the build user owns the browser cache;
the isolated substrate runs as root and otherwise searches its own cache.
`realtime` qualifies empty-workspace watch continuity over two real rotations
and verifies that connected observers do not prevent Project pause. The script builds
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


## Real-provider handoff

The `provider` suite is explicit and excluded from `all`. Supply a host-only YAML
file with `base_url` (an HTTPS OpenAI-compatible Chat Completions base), `api_key`
and `model`. Keep this file outside version control and restrict its permissions.

```sh
PROJECT_PROVIDER_CONFIG=/srv/qualification/provider.yaml dev/verify-khora.sh --template /srv/build/battery --manifest /srv/build/battery.manifest.json --output /srv/qualification/provider-run --suite provider
```

The host relay makes at most one provider request, caps generation at 1,536 tokens,
disables tools, and bounds time/response bytes. Native retries are disabled. The
network-isolated substrate reaches the relay through a private Unix socket and a
disposable namespace bridge; the actual provider key stays outside the guest and
its application/gateway environment. The relay does not throttle, synthesize or
replay the provider stream. Reports contain counts, timing, status and synthetic
attribution only, with no request/response text or credentials.

The test waits for genuine deltas, activates a distinct compatible owner while the
original stream is live, and requires the same gateway, one provider request, at
least ten seconds of streaming and one original native application/settlement/
completion sequence. It then deletes the disposable Project. Provider refusal,
short output, authority expiry, retries or missing native completion fail the gate.
See the [Chat Completions API reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
for the compatible streaming request shape; provider-specific access and behavior
must be qualified by the run itself.


## Retained-revision rollback

Build the prior compatible revision in a clean isolated checkout, stamping its
actual revision explicitly (some Go/worktree combinations omit automatic VCS
metadata):

```sh
CGO_ENABLED=0 go -C /srv/build/prior/orchestrator build -trimpath -ldflags='-X main.commit=FULL_PRIOR_COMMIT' -o /srv/qualification/prior-owner ./cmd/orchestrator
PROJECT_ROLLBACK_BINARY=/srv/qualification/prior-owner dev/verify-khora.sh --template /srv/build/battery --manifest /srv/build/battery.manifest.json --output /srv/qualification/rollback-run --suite rollback
```

The opt-in `rollback` suite starts the retained owner, upgrades to the current
candidate, accepts new native turns/held intent/file bytes, then explicitly rolls
back to the retained executable. Both gateway executables are exercised through
gated replacement in each direction. The test verifies running executable digests,
source revision declarations, preserved accepted data and subsequent native work.
It never restores a database backup.

To qualify the current client against that retained owner, run `--suite browser`
with `PROJECT_BROWSER_OWNER_BINARY=/srv/qualification/prior-owner`. The production
frontend build and browser prerequisites remain the same as the ordinary browser
gate. See the [compatibility matrix](../docs/compatibility.md) for the recorded pair
and the limits of that evidence.
