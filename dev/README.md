# Development helpers

[Development setup](../docs/development.md) describes authored-content publication
and the separate engine, application, model proxy and capture roles.

`verify-khora.sh` creates a disposable Linux namespace/cgroup/btrfs substrate,
builds or accepts the pinned engine, and runs real application tests. It takes a
finalized template directory and exported manifest; no private script, sibling
checkout, fixed account name or engine-owned runtime directory is required.
`--output` retains test/engine logs and application/engine/template identities.

```bash
export GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)"
sudo -E dev/verify-khora.sh \
  --template-dir /absolute/path/battery \
  --manifest /absolute/path/battery.manifest.json \
  --output /absolute/path/verification-output
```

The host needs Go, Python, runsc, iproute2, btrfs tools, Linux namespaces and cgroup
v2 delegation. Gateway process identity requires `SO_PEERPIDFD` (Linux 6.5+).
`--via-user` selects an existing non-root gateway identity (default `nobody`).
The template is bound read-only; engine state, volumes, cgroups, sockets and network
interfaces are disposable. Test fixtures deliberately use synthetic upstreams and
TEST-NET addresses. Packages sharing this engine run serially.

To measure bounded concurrent use, select `--run '^TestKhoraSustainedRetainedWorkload$'`.
The test runs at least 60 seconds with two sessions, 32 KiB output per turn, repeated
pause/resume and owner replacement during two eight-second turns. It checks exact
workspace counters and message identities, storage growth and released capacity,
then prints a `SUSTAINED_RESULT` JSON record. Capture and model-gateway interruption
are exercised by the separate composed model and network/capture tests.

`build-dns-pin.sh` is an optional rootfs build-host DNS helper. Production migration
and deployment acceptance remain a separate project.
