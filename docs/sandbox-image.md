# Immutable Project runtime image

The engine module is pinned to `v0.0.2-0.20260920024301-e8cd021a9c7c`.
The native release pins Pi `0.77.0` and Node `24.15.0`; source/package integrity
and runtime hashes are recorded by the Go release tooling.

```sh
sudo sandbox-image/build-khora-template.sh /srv/build/agent-runtime
```

The destination and its `.manifest.json` export must not exist. Build in a
disposable privileged Linux environment with the configured package mirrors,
Go module cache, debootstrap/chroot tooling and network access for pinned inputs.
The builder checks the engine pin before copying an optional base. It builds
sandboxd from that public module and Go application binaries from this checkout.

An optional second argument names a pure OS/Pi base filesystem:

```sh
sudo sandbox-image/build-khora-template.sh /srv/build/agent-runtime /srv/build/os-pi-base
```

An application template is not a valid base. Retired application state, secrets,
native user history or nonempty workspace/runtime directories fail generation.
Fresh assembly never mutates an existing release directory. Python stays in the
image for scientific workloads; it does not implement an application protocol.
See [build.env.example](../sandbox-image/build.env.example) and the locked
[Python requirements](../sandbox-image/requirements.lock) for workload inputs.

`runtime-release` generates `/etc/project-runtime/manifest.json` in the image and
an exported manifest beside it. It hashes sandboxd, all Go guest executables, Pi,
Node and builtin system context, and hashes the complete native dependency tree.
Native dependency symlinks must resolve inside that hashed tree. All ELF runtime
components must share a supported Linux architecture. The manifest records
engine/native/protocol pins, platform, bootstrap workspace/runtime defaults and
mount contract 1. Manifest version 5 records protected guest roots, image files
and symlinks that mounts must not shadow, and required publication roles with
fixed destinations and artifact identities. Generation reads this metadata from
the new image; allocation validates against the selected registered manifest
without starting a sandbox to inspect paths. Canonical non-default workspace and
runtime roots are supported. Required publication destinations remain fixed and
read-only. The saved Project plan supplies every guest role root to runner,
replay, file reader/mutations, Terminal, capture export and cleanup.

Manifest version 5 requires agent protocol 2 (bounded flow/pressure telemetry),
reader protocol 3 with per-request chunk credits and directory seek windows, and native input identity/settlement protocol 4.
A missing capability or an older manifest is rejected. Reader changes require
a newly assembled immutable template, not replacement of running guest binaries.

```sh
go -C orchestrator run ./cmd/runtime-release --root /srv/build/agent-runtime --output /srv/build/agent-runtime.manifest.json
```

Use the generated digest verbatim in application configuration. The application
loads the current exported manifest and checks its engine template platform. Before each placement gains child runtime authority, the host reads
`/etc/project-runtime/manifest.json`, validates its pinned digest and current
protocol requirements, then runs the image's `runtime-verify`. An older image
cannot establish compatibility merely by passing its own older verifier. Saved
Projects require their exact [registered release](configuration.md), retained
independently of later default selections.
Verification is idempotent by sandbox/epoch/digest and can reconcile a lost reply.
A component or native dependency mismatch refuses activation.

Image assembly alone is not runtime qualification. Run the actual application
and final image through the [isolated executable gates](verification.md), including
native Pi, Files, retained PTYs, cookie-authenticated Chromium and recovery.


The builder installs a narrowly pinned extension to Pi 0.77.0 RPC mode before
manifest generation. It checks the exact upstream and patched file SHA-256 values;
unknown input is refused. The extension emits a correlated `prompt_end` only when
the entire `session.prompt()` Promise settles, including retries and compaction.
`agent_end` remains an intermediate native event and is never completion authority.
Stop also cancels native retry and compaction, including preflight continuation.
The upstream package/version/integrity pin remains recorded alongside the complete
patched native tree. Generate and guest verification both require the exact native
RPC extension bytes; a matching tree digest alone cannot claim the capability.
Even comment-only changes to the generated extension change its pinned hash and
require a newly assembled runtime image for qualification. Changing only the
frontend display-name setting does not change the runtime image.

The patcher refuses an existing release or the host root. For manual fresh-root
assembly, use `runtime-release --patch-native --root ... --output ...`; generation
without that flag requires an already prepared native RPC module. Always assemble
a new immutable image. Existing format-2 templates cannot satisfy the new native
settlement contract, and changing the guest release requires a full admission drain.
