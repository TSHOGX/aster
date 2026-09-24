# Verification

Run the full Go race/vet suite and frontend typecheck/lint/tests/production build.
Package-boundary and removal tests check the supported architecture. Runtime
manifest tests reject changed components, native dependency escapes and mixed
binary platforms. Authentication, scoped children, journals, Files, schedules,
sharing and retirement have focused contract tests.

The network suite (`dev/verify-khora.sh --suite network` with the normal image,
manifest and output arguments) qualifies reload on the actual released owner and
pinned Khora/runsc. It checks guest DNS, a verified TLS download, denied undeclared
access, unchanged running placement, permission removal on resume, retained
volumes and native model routing. Its isolated endpoints do not certify public
Internet forwarding/NAT or a production package index.

Schedule/route browser regressions cover canonical global routes, reader-grant
selection, daily/weekly/monthly/manual/advanced forms, stale previews, uncertain
creates/runs, configuration versus tick conflicts, history budgets, late task
mutations, and both locales/themes through the content-width breakpoints.

Real gates use the pinned engine on a disposable Linux substrate:

1. Project provisioning, bounded admission, pause/resume and owned-volume cleanup.
2. Concurrent native Pi Sessions, journal replay, controls and uncertain dispatch.
3. Retained PTYs, input ownership, output gaps, EOF/signals and browser detach.
4. Confined Files under real runsc, symlink races, streaming and lost replies.
5. Service reachability, host-derived attribution, per-turn drain, independent
   gateway lifetime, bounded authority outage and gated gateway replacement.
6. Owner/worker/guardian/engine process faults and recovery/retirement settlement.
7. The actual released executable/image and cookie-authenticated Chromium journey.
8. A five-minute mixed workload with two Projects, four Agents, two retained PTYs,
   file read/write/watch traffic, scheduled turns and an impaired capture lane.

The mixed test records HTTP/ready-page latencies, combined owner/gateway RSS and sampled CPU, database
size, rounds, pending work, idle behavior and teardown. Targets are HTTP p95 under
1 second/p99 under 2 seconds, completion projection p95 under 2 seconds, healthy
work not delayed over 10 seconds by unrelated retries, RSS below 512 MiB, idle
pause within its configured deadline plus 5 seconds, owner restart within
15 seconds after dependencies are ready and teardown within 15 seconds excluding
injected delay. A failed target requires investigation, not a silent threshold
change.

The released-executable gate exercises four overlapping Pi Sessions on two
Projects, content publication, binding observation, actual owner SIGKILL/restart,
continued turns, scope rejection, idle pause and cleanup. The browser journey
covers single-conversation navigation with concurrent background Sessions, native Stop, CodeMirror file editing,
retained PTY detach/reload/input handoff, output gaps and fairness under output.
These use the final guest binaries, not injected application replacements.
The gateway fixture verifies one original upstream request and native completion
through an owner outage, followed by a quiescent gateway replacement. Its
deterministic model does not qualify a real-provider active update. The owner
handoff fixture additionally switches between distinct executable artifacts with
a matching contract while the original request remains active, preserves held
intent, and verifies one original native completion.

Use [dev/README.md](../dev/README.md) to run and collect a gate. Preserve source
revision, release digest, engine revision, platform, commands, test names and raw
outcomes with measurements. Review whether selected tests actually ran; a skipped
integration package or an empty test regex is not evidence.

Process-fault tests do not qualify physical power loss, cross-host restore or
active-active deployment. The supported deployment has one authoritative owner.

A sanitized [mixed-workload result](../dev/measurements/project-runtime.json)
records the qualified ARM64 workload and its sampling limits. These are measured
results for the declared workload, not hardware-independent service guarantees.

The frontend UI suite (`npm run test:ui`) separately checks per-project drafts,
first-send lost-response identity, paged navigation, dirty-file guards, layout
switches and fullscreen focus restoration. Its locale/theme/viewport captures
are UI evidence, not runtime integration evidence. The browser qualification
continues to exercise the real Go edge and native PTYs after selector changes.


The `native` suite uses the actual pinned Pi and released guardian. It injects a
retryable provider failure, checks that the original turn stays running and queued
work waits, then tests cancellation during retry and compaction. A successful
compaction must precede `prompt_end` and command completion, and its entry must be
present in the exported native capture. Preflight cancellation must dispatch no
new native agent run. Manifest tests refuse a claimed settlement capability whose
RPC bytes are absent or changed.


The opt-in `provider` suite performs a bounded real Chat Completions request with
native retries disabled, activates a distinct compatible owner during genuine
streamed output, and requires at least ten seconds of streaming, one provider
request and one original native completion. A [recorded GLM-5.2 run](../dev/measurements/owner-handoff.json)
passes this gate. Credentials and prompt/response text are excluded from its
reports. Synthetic and failed provider trials do not substitute for this evidence.


Mixed-workload quantiles use nearest rank. Projection samples are taken after a
ready page is observed, not when native turn state first becomes completed. Resource
accounting includes the separate gateway and sums sampled CPU maxima across owner
process replacement. See the [accounting-corrected observation](../dev/measurements/mixed-accounting.json)
for values and limits; it is not the complete browser/shaped-network acceptance matrix.


The opt-in `combined` suite runs the five-minute native workload alongside two
production browser tabs, repeated conversation/preview selections, an actual file
edit, a retained unsent draft and a non-reading SSE client with a small TCP receive
window. Four native fixture streams overlap a gated switch to a distinct compatible
owner while the gateway remains stable. The deliberately impaired capture lane is
repaired before requesting readiness. Original request identity/completion and held
intent are checked after the update. This suite uses synthetic provider responses.

Browser profiles separately use loopback and CDP request latency of 100 ms with
10 Mbps upload/download limits. CDP emulation is not a packet-level RTT profile.
Each profile records one warmup and thirty sequential warmed selection, cached
return and small text preview samples. Timing starts at browser pointer-down and
ends at the first animation frame with useful DOM; it does not measure paint or
field INP. Raw samples, failures, request counts, long tasks and post-GC checkpoints
are retained. These repeated warmed journeys do not replace independent cold
loads, Office distributions or controlled baseline comparisons.

Browser setup may outlast the fixture's five-second Agent idle timeout. A second
explicit native warmup therefore precedes the measured workload. Cold provisioning
and warmup remain outside that latency distribution. The existing mixed workload
keeps its original owner-crash scenario; combined handoff is a separate scenario.
The receive window applies real socket backpressure, but queue overflow is only
claimed when separately observed. Owner/gateway RSS and sampled CPU are separate
from browser heap/DOM measurements.


The combined test also records separate 60/120-second post-ready browser idle
windows with selected resources quiet while PTYs and unrelated schedules continue.
It requires zero heavy reads, zero new preview URLs and stable preview DOM identity.
The [recorded run](../dev/measurements/combined-workload.json) includes a passing
result and the earlier warmed fairness failure; it does not claim a complete
cross-phase performance matrix. Per-round timing breakdowns are in
`round-samples.json`; source/provider timings remain separate from display readiness.


Projection observation now runs independently for each Session every 250 ms,
including scheduled and handoff turns. Only a coherent ready page containing the
completed turn counts. Every provider completion within the offered workload must
be observed; missing samples fail the gate. Pollers stop before quiet-window
measurement. Their bounded requests, all attempts, failures and latencies are
reported separately as `projection_observer`, preserving the existing HTTP
sample distribution. This removes delays caused by waiting for a later regular
mutation round; the two-second projection-observation budget is unchanged.


The projected-query scale test uses one warmup and thirty samples per size. Run
`PROJECT_DISPLAY_SCALE=1 go test ./internal/product -run '^TestDisplayReadScale$' -count=1 -timeout 3m -v`
from `orchestrator/` to include 100,000 synthetic records. It requires invariant
visible content, an indexed page plan and a logical database below 1 GiB. The
[comparison record](../dev/measurements/conversation-scale.json) includes a retained
raw-query baseline without restoring that path to production.


For independent ordinary Office timings, provide `PREVIEW_FIXTURES` and a new
`OFFICE_LATENCY_OUTPUT`, then run the opt-in `office-latency.spec.ts` UI suite.
It records thirty fresh contexts per format/CPU profile plus one warmup; production
assets are built by the UI harness. Keep routed-body timing separate from network
or real runtime qualification.

For the maximal transcript scroll profile, set `HISTORY_WINDOW_OUTPUT`,
`HISTORY_SCROLL_PROFILE=1` and optionally `HISTORY_CPU_RATE=4`, then select
`navigation-budget.spec.ts` with the `maximal transcript` test. CPU profiling is
kept separate from ordinary latency trials. Preserve its raw profile and metadata;
DOM inspection performed by test automation is not application attribution.


`startup-budget.spec.ts` is opt-in with `STARTUP_PROFILE_OUTPUT`. It records thirty
fresh-context initialized journeys plus a separate real elapsed 60/120-second
quiet window. `STARTUP_FIXTURE`, `STARTUP_ORIGIN` and `STARTUP_BUILD_DIRECTORY`
select a disposable retained build; `STARTUP_PAIRED_FIXTURE` pairs that build on
loopback port 3108 with the current one on 3107, alternating order. Never select
live user state. Preserve dataset digests, exact build IDs and all failed or slower
attempts. The [startup record](../dev/measurements/startup-comparison.json) explains
why action wall time and browser-local feedback are different measurements.

`INTERACTION_BENCHMARK_OUTPUT` enables `interaction-overhead.test.ts` to measure
bounded timing-ring call overhead in Node. Keep that microbenchmark separate from
browser latency trials; its result is not browser instrumentation overhead.


The mixed/combined harness retains one-second body-free diagnostic samples in
`mixed-telemetry.jsonl`: writer admission/wait peaks, Agent ACK/journal pressure,
runtime leases and read resources. Scheduling ticks precede the bounded RPC so a
stalled observation is distinguishable from missing sampling. Host pressure-stall
information is recorded only where `/proc/pressure` exists; otherwise its absence
is explicit. These additional observations do not relax existing pass budgets.

The confined Files gate also records fifty warm capture latencies, host CPU ticks,
file-descriptor checkpoints and final reader lease/capture counts. This scope
excludes engine and guest CPU. Required correctness remains zero per-read database
writes, one retained helper and no unreleased captures or leases.


Concurrent file read capacity rejections are counted separately from successful
readback. The mixed harness retries only an explicit read-only 503 capacity reply
for at most two seconds with 25 ms backoff; it never retries mutations or accepts
wrong bytes. Rejected attempts remain in the report and the whole-round budget
still applies. Exhausted retry time fails the gate.


Final reports distinguish `provider_completion_to_view_p95_ms` from
`projection_p95_ms`. The former starts when the provider fixture finishes HTTP
streaming and includes native settlement, capture, transport and ingestion. The
latter starts at the authoritative turn completion timestamp recorded in its
committing transaction and ends at first ready-page observation. It includes
commit and sampling overhead and must meet the existing two-second projection
budget. Both raw distributions are retained; the ten-second round budget still
covers the complete offered work. Older reports used the provider endpoint as an
upper bound and must not be relabeled as the newer projection definition.

The [navigation/scheduling/network qualification](../dev/measurements/proposal-delivery.json)
records schema 15, the exact runtime/engine pin, built artifacts and tested source
identities. It includes the final 137-pass UI sweep, 186-pass unit suite, real
network lifecycle gate, six release/runtime checks and the composed browser
journey. Opt-in skips and public-Internet/usability limits are explicit. Earlier
failures and their fixes are retained in the record rather than relabeled as
initial passes.

The subsequent [fresh-deployment record](../dev/measurements/fresh-deployment.json)
supersedes the prior schema-15 installation boundary. It qualifies the canonical
schema, selected-executable activation, rebuilt runtime and deployed data reset.
Old migration/rollback suites were removed; current initialization/refusal and
activation tests cover the new contract.

`npm run test:pickers` runs Schedule picker interactions in Chromium, Firefox and
WebKit against production assets. Install those Playwright browsers and their
platform dependencies first. Checks cover Interval-first order, timezone-arrow
selection/search, hour/minute confirmation, typed time, saved values, keyboard
focus/Tab/Escape, and narrow English/Chinese light/dark layouts. These are browser
engine checks on the test host, not a claim of testing every operating system.
