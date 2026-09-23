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

The mixed test records HTTP/projection latencies, application RSS/CPU, database
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
