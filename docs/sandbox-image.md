# Khora template

`sandbox-image/build-khora-template.sh` builds a template containing the pinned
sandboxd, Pi and shell drivers, launcher, workspace helper and journal recovery reader.
The base rootfs builder installs generic tools and driver packages. The finalizer
writes an immutable component manifest and pins the exact Khora module/revision.

HOME is `/root`; writable Pi state stays in `/root/.pi` in the retained overlay.
`/workspace`, `/harness-bridge` and `/harness-config` are distinct mounts; shared assets
mount read-only at `/root/.agents`. The process bridge uses framed stdio and a durable
journal. No file queue, checkpoint-ready marker or legacy init entrypoint is installed.

Shared assets are maintained manually in [shared-assets](../sandbox-image/files/usr/local/share/shared-assets).
They are versioned source inputs, not automatically distilled or evolved. Deployment
specific datasets and credentials are private. Launch plans currently require supplied
immutable volume references; automated durable publication is not yet implemented.

Builders may configure package mirrors and DNS through the private build environment.
Templates and build outputs must contain no host-specific credentials or production data.
