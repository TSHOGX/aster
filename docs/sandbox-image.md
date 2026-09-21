# Khora template

`sandbox-image/build-khora-template.sh` builds a template containing the pinned
sandboxd, the Pi driver, launcher, workspace helper and journal recovery reader.
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

## Build and export

Run the builder as root inside a disposable Linux build environment with package
network access, Go and the pinned module available. The destination must not exist:

```bash
sudo env "PATH=$PATH" "GOPATH=$(go env GOPATH)" "GOCACHE=$(go env GOCACHE)" \
  bash sandbox-image/build-khora-template.sh /absolute/path/templates/battery
```

The builder rejects ignored files in `sandbox-image/files`, installs both Pi and
the Pi driver, builds the pinned `sandboxd`, and finalizes component hashes. It exports:

- `/absolute/path/templates/battery`: template directory for engine registration.
- `/absolute/path/templates/battery.manifest.json`: manifest consumed by the daemon.
- `/absolute/path/templates/battery.manifest.sha256`: canonical JSON digest; prefix
  its value with `sha256:` for deployment `manifest_digest`.

The manifest inside the template is `etc/harness-image/agents.json`. Its canonical
digest excludes formatting such as the trailing newline, so a raw `sha256sum` of
the JSON file is not the deployment digest. Build a new revision directory when
components change; the launch contract checks the pinned component hashes.

The Khora wrapper uses `SANDBOX_BUILD_ENV=/dev/null`; it does not load a private
build-env file automatically. Review [build-rootfs.sh](../sandbox-image/build-rootfs.sh)
for supported exported environment settings. Use the exported template/manifest
with the [isolated harness](../dev/README.md).
