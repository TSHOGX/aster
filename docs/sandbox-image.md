# Immutable Project runtime image

The engine module is pinned to `v0.0.2-0.20260920024301-e8cd021a9c7c`.
The native release pins Pi `0.77.0` and Node `24.15.0`; source/package integrity
and runtime hashes are recorded by the Go release tooling.

```sh
sudo sandbox-image/build-khora-template.sh /srv/build/battery
```

The destination and its `.manifest.json` export must not exist. Build in a
disposable privileged Linux environment with the configured package mirrors,
Go module cache, debootstrap/chroot tooling and network access for pinned inputs.
The builder checks the engine pin before copying an optional base. It builds
sandboxd from that public module and Go application binaries from this checkout.

An optional second argument names a pure OS/Pi base filesystem:

```sh
sudo sandbox-image/build-khora-template.sh /srv/build/battery /srv/build/os-pi-base
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
engine/native/protocol pins, platform and required workspace/runtime paths.
Manifest version 2 requires agent protocol 2 (bounded flow/pressure telemetry)
and reader protocol 2, including per-request chunk
credits; a manifest missing that capability is rejected. Reader changes require
a newly assembled immutable template, not replacement of running guest binaries.

```sh
go -C orchestrator run ./cmd/runtime-release --root /srv/build/battery --output /srv/build/battery.manifest.json
```

Use the generated digest verbatim in application configuration. The application
loads the exported manifest, checks the engine template platform and runs the
image's `runtime-verify` before each placement gains child runtime authority.
Verification is idempotent by sandbox/epoch/digest and can reconcile a lost reply.
A component or native dependency mismatch refuses activation.

Image assembly alone is not runtime qualification. Run the actual application
and final image through the [isolated executable gates](verification.md), including
native Pi, Files, retained PTYs, cookie-authenticated Chromium and recovery.
