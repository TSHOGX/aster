# Development

Use a dedicated Linux host or VM for runtime development. The orchestrator uses
Linux-only syscalls and invokes runsc, ip and nft. The frontend can be developed
separately, but a full runtime/model smoke needs the Linux service and an external
model proxy.

## Private configuration

Create an ignored `private/development/` directory, copy `config/harness.yaml` as
a starting point and set all data/run/network roots for your environment. The
checked-in configuration is a generic example, not a production deployment.
Do not share a live DB, runsc root, network pool or service port with another
installation. Keep authentication, title/memory LLM files and gateway secrets in
the same private configuration directory or explicit external paths.

`--config PATH` / `HARNESS_CONFIG_PATH` select the application YAML; environment
variables override the supported runtime fields. Explicit missing files fail.
See [configuration](configuration.md).

## Build and run

```bash
go -C orchestrator build -o bin/orchestrator ./cmd/orchestrator
# Use the target's required privileges and explicit environment settings.
orchestrator/bin/orchestrator --config /absolute/private/development/harness.yaml

cd frontend
npm ci
HARNESS_API_BASE_URL=http://127.0.0.1:8090 PORT=8000 npm run dev
```

Ports are examples. Use only an isolated loopback setup for unmanaged open auth;
configure credentials and TLS for shared access. `HARNESS_DEV_ORIGINS` is a
comma-separated list of development browser origins and should be locally set.

To build a rootfs on a Linux builder with root privileges and debootstrap:

```bash
sudo SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh
```

An existing rootfs is reused by default and receives overlay/manifest updates;
`FORCE=1` rebuilds it and removes that destination. Never point the build script at
a live rootfs. Host-specific build mirrors and DNS hooks belong in an ignored
build environment file. See [sandbox image](sandbox-image.md).

## Model proxy adapter

`dev/model-proxy/proxy.py` is a development forwarding adapter. Copy its
`.env.example` into private configuration, provide your own upstream settings and
point correlation at this deployment's Unix socket. The adapter authorizes model
requests before forwarding with its host-only key. Do not pass real upstream keys
into sandbox environment variables.

The adapter and `dev/build-dns-pin.sh` are optional tools, not instructions for a
specific VM, resolver or cloud. This repository installs no machine-specific
startup/shutdown service. Stop the processes you started through your own service
manager and use the runtime cleanup contract for sandboxes; avoid kill-by-port or
blanket network cleanup on a shared host.

## Working on changes

Branch from current main and keep changes independently reviewable. Git worktrees
are useful for unrelated work. Run relevant checks before opening a PR and include
the trigger, resulting behavior and validation. Keep plans in Issues and update
current reference docs with implementation, not proposal histories.

[Verification](verification.md) lists the test commands and release checks.
