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

Suites are `release`, `files`, `network`, `gateway`, `native`, `provider`, `mixed`, `combined`, `browser`, `realtime`, `faults` and `all`. The default is the
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

`all` includes release, files, network, mixed, browser, realtime and faults;
gateway/native are release subsets and provider/combined remain explicit.
Only the selected Go test packages are built. Empty test selections fail.

Logs, revision metadata, screenshots and workload measurements go to a new explicit
output directory; existing directories are refused to prevent mixed evidence.
The disposable filesystem is removed after namespace teardown;
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


Run the opt-in combined browser/native workload with the same production frontend
and Chromium prerequisites:

```sh
PLAYWRIGHT_BROWSERS_PATH=/srv/playwright dev/verify-khora.sh --template /srv/build/battery --manifest /srv/build/battery.manifest.json --output /srv/qualification/combined-run --suite combined
```

It retains the five-minute offered-load budget and adds two browser tabs, loopback
and CDP network profiles, a slow event reader, and a guarded active-owner update.
It is excluded from `all` because it is a separate longer measurement scenario.
See the [measurement definitions](../docs/verification.md). Inspect `browser.log`,
`combined-browser.json`, `combined-handoff.json` and `mixed-metrics.json` together;
partial samples or setup-only runs are not successful qualification. Failure-state
records retain only synthetic identifiers/states and native error evidence.

Every harness invocation records source hashes (including uncommitted files),
executable/engine/runtime-manifest hashes, platform, CPU/memory, Go/Node versions and the frontend build ID
in `fingerprint.json`. Preserve the actual binaries and frontend build together
with this metadata; source hashes alone cannot recreate an uncommitted build.

For [candidate qualification](../docs/delivery.md), add `--candidate DIR`.
The bundle inventory must verify and its commit must match the clean checkout.
The actual packaged owner and standalone frontend are used; the candidate manifest
hash enters `fingerprint.json`. The source checkout still supplies Playwright and
test/alternate-owner fixtures. A development run without this option does not
certify a separately built candidate bundle.


The `files` suite exercises the real released application and Khora volume APIs:
initial upload before compute exists, native conversation completion, idle pause,
continued conversation/file access, paused edits/rename/delete, empty and chunked
uploads, snapshot pagination, and resume with retained workspace bytes. It asserts
zero occupied Projects throughout paused file operations.

## Network policy lifecycle

`--suite network` runs the released owner and pinned Khora/runsc with isolated DNS
and TLS package endpoints. It verifies reload while running leaves the binding
and guest projection unchanged, resume applies matching permissions/DNS, stored
workspace/runtime volumes survive, undeclared destinations remain denied, removal
applies at the next resume, and the retained model gateway completes a native
turn. `network-policy.json` distinguishes this local substrate evidence from
public Internet/package-index reachability, which it does not test.
