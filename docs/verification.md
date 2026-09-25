# Verification

Run from the repository root:

```sh
go -C orchestrator test ./...
go -C orchestrator test -race ./internal/product ./internal/agentrunner ./internal/runtimerelease
go -C orchestrator vet ./...
python3 scripts/check-public-docs.py
cd frontend
npm run typecheck
npm run lint
npm test
npm run build
npm run test:ui
```

## Native and application qualification

Build a new immutable template with `sandbox-image/build-khora-template.sh`.
The source and patched-output digests must match the pinned runtime adapter.
Use [the isolated harness](../dev/README.md) with the exact manifest and newly
built engine. It creates its own btrfs filesystem, namespaces and test database.
It must never attach the installed application's database or engine store.

`release` covers executable identity, owner handoff, gateway continuity, real Pi
retry/compaction, queued Steer consumption, Stop removal and next-run non-leak.
`browser` exercises the built frontend against the real API and native runtime,
including two-tab Steer handoff. `network` validates accepted policy snapshots.
`faults` covers process/journal recovery, workspace mutation, PTY and service
authority. `mixed` and `combined` add sustained workload and two-tab activity.
A real-provider check is opt-in; normal qualification uses controlled streams.

## Evidence invariants

- A successful receipt is durable admission, not proof of consumption.
- One prompt has one effective consumption/handled claim; unknown is not reusable.
- Stop preserves its original Run target and draft, including while another row
  or ordinary submission is pending.
- Core queue cleanup failure emits no successful reconciliation. The runner
  terminates that process and recovers original journal evidence.
- Observer failure after proven queue clearing cannot strand settlement.
- Native user events cannot relocate a consumed input anchor. There is no
  historical-message fallback or old-format conversion path.
- Segment state follows its own boundary and the correlated Run; late tool
  updates keep original ownership. Unknown Runs do not leave running clocks.
- Page, delta, reconnect, rebuild, share and export chronology agree.
- Current schema fingerprint and protocol are mandatory. Incompatible data is
  discarded only by the explicitly authorized installation reset.

## Live reset acceptance

Verify stopped supervisors and absence of descendants before deleting owned
state. Record deletion counts, new engine Store ID, candidate digests and empty
product tables before smoke tests. Verify preserved credentials without logging
their contents. Exercise login, Project provisioning, workspace read/write,
scheduling, native model completion and deployed browser controls, then remove
the temporary user and Project. Check service restart counts and final runtime
identity after smoke cleanup. Publish only sanitized current evidence.
