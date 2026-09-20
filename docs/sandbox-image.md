# Khora template

`sandbox-image/build-khora-template.sh` builds a template containing the pinned
sandboxd, Pi and shell drivers, launcher, workspace helper and journal recovery reader.
The base rootfs builder installs generic tools and driver packages. The finalizer
writes an immutable component manifest and pins the exact Khora module/revision.

HOME is `/root`; writable Pi state stays in `/root/.pi` in the retained overlay.
`/workspace`, `/harness-bridge` and `/harness-config` are distinct mounts; shared assets
mount read-only at `/root/.agents`. The process bridge uses framed stdio and a durable
journal. No file queue, checkpoint-ready marker or legacy init entrypoint is installed.

Shared assets and agent configuration are authored source trees outside the rootfs:
[shared-assets](../config/shared-assets) and a copy of
[agent-config.example](../config/agent-config.example). They are versioned source
inputs, never automatically distilled or evolved. The daemon reads each tree,
computes its revision digest and publishes it to an immutable engine volume before
accepting work; a launch mounts only a revision this engine store has published.
Deployment-specific datasets and credentials stay private.

Builders may configure package mirrors and DNS through the private build environment.
Templates and build outputs must contain no host-specific credentials or production data.
