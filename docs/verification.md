# Verification

Run the full Go race/vet suite and frontend typecheck/lint/tests/production build.
Package-boundary and removal tests check the supported architecture. Runtime
manifest tests reject changed components, native dependency escapes and mixed
binary platforms. Authentication, scoped children, journals, Files, schedules,
sharing and retirement have focused contract tests.

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
